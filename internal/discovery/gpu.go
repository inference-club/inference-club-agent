// Live GPU stats for the cluster viz — the nvtop half of /cluster/state.
//
// The k8s API only knows GPU *count* (nvidia.com/gpu allocatable); it never
// sees live VRAM or utilization. Those come from dcgm-exporter, deployed as a
// DaemonSet exposing per-GPU metrics on hostPort 9400 of each GPU node (see
// clusters/home/monitoring/dcgm-exporter.yaml in the home-cluster repo). We
// scrape each node's InternalIP:9400 directly — plain HTTP, no auth — and parse
// the three metrics that matter:
//
//   DCGM_FI_DEV_FB_USED   framebuffer used, MiB
//   DCGM_FI_DEV_FB_FREE   framebuffer free, MiB  (used+free = total)
//   DCGM_FI_DEV_GPU_UTIL  SM utilization, percent
//
// Scraping is best-effort and per-node: a box without dcgm (a2 parked, spark
// needs an arm64 image) simply yields GPU=nil, the same degrade-don't-fail
// contract metrics_available uses for memory.
package discovery

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const mib = int64(1) << 20

// scrapeNodeGPUs scrapes dcgm-exporter on every node that has an InternalIP,
// concurrently. Returns a map keyed by node name; nodes without reachable dcgm
// (or when DCGMPort<=0) are simply absent. Never returns an error — GPU stats
// are progressive enhancement, not a precondition for the snapshot.
func (k *Kubernetes) scrapeNodeGPUs(ctx context.Context, nodes []k8sNode) map[string]*NodeGPU {
	out := map[string]*NodeGPU{}
	if k.DCGMPort <= 0 {
		return out
	}
	// Plain-HTTP client with a tight timeout: 9400 is unauthenticated on the
	// node, and a slow/absent exporter must not stall the whole snapshot.
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
			gpu, err := scrapeDCGM(ctx, client, ip, k.DCGMPort)
			if err != nil || gpu == nil {
				return
			}
			mu.Lock()
			out[name] = gpu
			mu.Unlock()
		}(n.Metadata.Name, ip)
	}
	wg.Wait()
	return out
}

func internalIP(n k8sNode) string {
	for _, a := range n.Status.Addresses {
		if a.Type == "InternalIP" {
			return a.Address
		}
	}
	return ""
}

// scrapeDCGM fetches and parses dcgm-exporter's /metrics for one node.
func scrapeDCGM(ctx context.Context, client *http.Client, ip string, port int) (*NodeGPU, error) {
	url := "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/metrics"
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
	return parseDCGM(resp.Body)
}

// parseDCGM reads the Prometheus text exposition and aggregates the three
// metrics per GPU index into a NodeGPU. Returns nil if no GPU lines were found
// (so a reachable-but-GPU-less exporter doesn't manufacture an empty card).
func parseDCGM(r io.Reader) (*NodeGPU, error) {
	type acc struct {
		usedMiB, freeMiB int64
		util             int
		model            string
		seen             bool
	}
	byIdx := map[int]*acc{}
	get := func(idx int) *acc {
		a := byIdx[idx]
		if a == nil {
			a = &acc{}
			byIdx[idx] = a
		}
		a.seen = true
		return a
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		isUsed := strings.HasPrefix(line, "DCGM_FI_DEV_FB_USED")
		isFree := strings.HasPrefix(line, "DCGM_FI_DEV_FB_FREE")
		isUtil := strings.HasPrefix(line, "DCGM_FI_DEV_GPU_UTIL")
		if !isUsed && !isFree && !isUtil {
			continue
		}
		idx, ok := labelInt(line, "gpu")
		if !ok {
			continue
		}
		val, ok := metricValue(line)
		if !ok {
			continue
		}
		a := get(idx)
		if model := labelStr(line, "modelName"); model != "" && a.model == "" {
			a.model = model
		}
		switch {
		case isUsed:
			a.usedMiB = int64(val)
		case isFree:
			a.freeMiB = int64(val)
		case isUtil:
			a.util = int(val)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(byIdx) == 0 {
		return nil, nil
	}

	idxs := make([]int, 0, len(byIdx))
	for idx := range byIdx {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	g := &NodeGPU{}
	for _, idx := range idxs {
		a := byIdx[idx]
		used := a.usedMiB * mib
		total := (a.usedMiB + a.freeMiB) * mib
		g.Devices = append(g.Devices, GPUDevice{
			Index:          idx,
			Model:          a.model,
			VRAMUsedBytes:  used,
			VRAMTotalBytes: total,
			UtilPercent:    a.util,
		})
		g.VRAMUsedBytes += used
		g.VRAMTotalBytes += total
		g.UtilPercent += a.util
	}
	if n := len(g.Devices); n > 0 {
		g.UtilPercent /= n // average across the node's GPUs
	}
	return g, nil
}

// labelStr extracts a string label value: key="value". Empty if absent.
func labelStr(line, key string) string {
	at := strings.Index(line, key+`="`)
	if at < 0 {
		return ""
	}
	rest := line[at+len(key)+2:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func labelInt(line, key string) (int, bool) {
	s := labelStr(line, key)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// metricValue returns the trailing sample value (the last whitespace-delimited
// field, after the closing "}"). dcgm emits integers but we parse as float to
// be tolerant, then truncate.
func metricValue(line string) (float64, bool) {
	brace := strings.LastIndexByte(line, '}')
	tail := line
	if brace >= 0 {
		tail = line[brace+1:]
	}
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
