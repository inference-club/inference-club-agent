// Per-process VRAM with pod attribution — the breakdown dcgm-exporter can't
// give (it sees per-GPU totals, never which process holds them, so it can't
// split a GPU shared by two services). A vram-reporter DaemonSet runs on each
// GPU node, joins `nvidia-smi --query-compute-apps` to the pod owning each pid
// (via hostPID + the node's pods), and serves the result at :9401/vram. We
// scrape each node's InternalIP:9401 directly — same plain-HTTP, best-effort,
// degrade-don't-fail contract as the dcgm scrape in gpu.go.
//
// The reporter resolves pid→pod locally (only it can — it's on the node); the
// agent adds the cluster-wide pod→managed-service step in assembleState.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// reporterPayload is the JSON the vram-reporter serves at :<VRAMPort>/vram.
type reporterPayload struct {
	Processes []reporterProcess `json:"processes"`
	Devices   []reporterDevice  `json:"devices"`
}

// reporterDevice is a per-GPU device summary (utilization + framebuffer).
type reporterDevice struct {
	Index         int    `json:"index"`
	UUID          string `json:"uuid"`
	UtilPercent   int    `json:"util_percent"`
	MemUsedBytes  int64  `json:"mem_used_bytes"`
	MemTotalBytes int64  `json:"mem_total_bytes"`
}

// nodeVRAM bundles a node's reporter scrape: per-process VRAM, node-level
// device utilization, and the summed device framebuffer. MemTotalBytes==0 with
// processes present means unified memory (Spark) — there's no framebuffer; a
// nonzero total means a discrete GPU (used to give a2 a real VRAM bar when
// dcgm isn't deployed there).
type nodeVRAM struct {
	Processes     []GPUProcess
	UtilPercent   int
	MemUsedBytes  int64
	MemTotalBytes int64
}

// reporterProcess is one GPU compute process with its owning pod resolved.
// UsedBytes is bytes (the reporter converts nvidia-smi's MiB).
type reporterProcess struct {
	PID         int    `json:"pid"`
	GPUIndex    int    `json:"gpu_index"`
	GPUUUID     string `json:"gpu_uuid"`
	UsedBytes   int64  `json:"used_bytes"`
	ProcessName string `json:"process_name"`
	Pod         string `json:"pod"`
	Namespace   string `json:"namespace"`
}

// scrapeNodeVRAM scrapes the vram-reporter on every node with an InternalIP,
// concurrently. Returns per-node []GPUProcess (Service left for the agent to
// fill). Never errors: unreachable nodes — or VRAMPort<=0 — are simply absent.
func (k *Kubernetes) scrapeNodeVRAM(ctx context.Context, nodes []k8sNode) map[string]*nodeVRAM {
	out := map[string]*nodeVRAM{}
	if k.VRAMPort <= 0 {
		return out
	}
	client := &http.Client{Timeout: 2 * time.Second}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		ip := internalIP(n)
		if ip == "" {
			continue
		}
		wg.Add(1)
		go func(name, ip string) {
			defer wg.Done()
			nv, err := scrapeVRAM(ctx, client, ip, k.VRAMPort)
			if err != nil || nv == nil || len(nv.Processes) == 0 {
				return
			}
			mu.Lock()
			out[name] = nv
			mu.Unlock()
		}(n.Metadata.Name, ip)
	}
	wg.Wait()
	return out
}

// scrapeVRAM fetches and decodes one node's reporter payload.
func scrapeVRAM(ctx context.Context, client *http.Client, ip string, port int) (*nodeVRAM, error) {
	url := "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/vram"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var payload reporterPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	procs := make([]GPUProcess, 0, len(payload.Processes))
	for _, p := range payload.Processes {
		procs = append(procs, GPUProcess{
			PID:         p.PID,
			GPUIndex:    p.GPUIndex,
			GPUUUID:     p.GPUUUID,
			UsedBytes:   p.UsedBytes,
			ProcessName: p.ProcessName,
			Pod:         p.Pod,
			Namespace:   p.Namespace,
		})
	}
	nv := &nodeVRAM{Processes: procs}
	if n := len(payload.Devices); n > 0 {
		var sum int
		for _, d := range payload.Devices {
			sum += d.UtilPercent
			nv.MemUsedBytes += d.MemUsedBytes
			nv.MemTotalBytes += d.MemTotalBytes
		}
		nv.UtilPercent = sum / n // average across the node's GPUs
	}
	return nv, nil
}
