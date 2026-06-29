// vram-reporter — a tiny per-node DaemonSet that answers "which pod is using
// this GPU's memory, and how much". dcgm-exporter gives per-GPU totals but
// can't attribute them to a process, so it can't split a GPU shared by two
// services. This can: it runs `nvidia-smi --query-compute-apps` for the
// per-process VRAM, then joins each pid to its pod via the process cgroup
// (requires hostPID) and the node's pod list from the Kubernetes API.
//
// It serves the result as JSON at :PORT/vram (default 9401, hostPort). The
// inference-club agent scrapes every node's :9401 and adds the cluster-wide
// pod→managed-service step (see internal/discovery/vram.go). Stdlib only,
// matching the agent's no-client-go philosophy.
//
// Required env (set by the DaemonSet):
//
//	NODE_NAME   the node this pod runs on (fieldRef spec.nodeName)
//
// Optional:
//
//	PORT        listen port (default 9401)
//	NVIDIA_SMI  nvidia-smi path (default "nvidia-smi")
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	saDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	mib   = int64(1) << 20
)

// process is one GPU compute process with its owning pod resolved. The JSON
// shape is the cross-repo contract with the agent's reporterProcess.
type process struct {
	PID         int    `json:"pid"`
	GPUIndex    int    `json:"gpu_index"`
	GPUUUID     string `json:"gpu_uuid"`
	UsedBytes   int64  `json:"used_bytes"`
	ProcessName string `json:"process_name"`
	Pod         string `json:"pod"`
	Namespace   string `json:"namespace"`
}

// device is a per-GPU summary. UtilPercent is device-level SM utilization
// (per-process util isn't exposed on GeForce/GB10 — pmon returns "-"), which
// is the one utilization signal available uniformly, including on the Spark
// where dcgm can't run.
type device struct {
	Index       int    `json:"index"`
	UUID        string `json:"uuid"`
	UtilPercent int    `json:"util_percent"`
	// Device framebuffer, bytes. Real on discrete GPUs; 0 on unified memory
	// (GB10), where nvidia-smi reports memory.total as "[N/A]" — that 0 is how
	// the agent tells a discrete-without-dcgm node (a2) from a unified one.
	MemUsedBytes  int64 `json:"mem_used_bytes"`
	MemTotalBytes int64 `json:"mem_total_bytes"`
}

type payload struct {
	Processes []process `json:"processes"`
	Devices   []device  `json:"devices"`
}

func main() {
	port := envDefault("PORT", "9401")
	node := os.Getenv("NODE_NAME")
	if node == "" {
		log.Fatal("NODE_NAME is required (set it from spec.nodeName via fieldRef)")
	}

	kc, err := newK8s()
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/vram", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		procs, err := collect(ctx, kc, node)
		if err != nil {
			// Degrade-don't-fail: an empty payload is a valid "nothing resident
			// / nvidia-smi unavailable" answer, same contract as the dcgm scrape.
			log.Printf("collect: %v", err)
			procs = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload{Processes: procs, Devices: devices(ctx)})
	})
	// /metrics is the same data as /vram in Prometheus text-exposition format, so
	// the cluster's static-scrape Prometheus (like dcgm on :9400, node-exporter
	// on :9100) can graph per-service VRAM directly. Stdlib only.
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		procs, err := collect(ctx, kc, node)
		if err != nil {
			log.Printf("collect: %v", err)
			procs = nil
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeMetrics(w, node, procs, devices(ctx))
	})

	addr := ":" + port
	log.Printf("vram-reporter listening on %s (node %s)", addr, node)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// collect gathers the per-process VRAM and attributes each process to a pod.
func collect(ctx context.Context, kc *k8sClient, node string) ([]process, error) {
	apps, err := computeApps(ctx)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}
	uuidToIndex := gpuIndexByUUID(ctx) // best-effort; missing → index -1

	pods, err := kc.podsOnNode(ctx, node)
	if err != nil {
		// Without the pod list we can still report VRAM, just unattributed.
		log.Printf("list pods on %s: %v", node, err)
		pods = nil
	}
	byUID, byContainer := podIndexes(pods)

	out := make([]process, 0, len(apps))
	for _, a := range apps {
		uid, cid := podRefForPID(a.pid)
		var pod podRef
		ok := false
		if p, hit := byContainer[cid]; hit && cid != "" {
			pod, ok = p, true
		} else if p, hit := byUID[uid]; hit && uid != "" {
			pod, ok = p, true
		}
		idx := -1
		if i, hit := uuidToIndex[a.uuid]; hit {
			idx = i
		}
		pr := process{
			PID:         a.pid,
			GPUIndex:    idx,
			GPUUUID:     a.uuid,
			UsedBytes:   a.usedMiB * mib,
			ProcessName: a.name,
		}
		if ok {
			pr.Pod, pr.Namespace = pod.name, pod.namespace
		}
		out = append(out, pr)
	}
	return out, nil
}

// --- prometheus exposition --------------------------------------------------

// writeMetrics renders the per-process VRAM and per-device stats as Prometheus
// text exposition. Per-process rows are summed by
// (gpu, uuid, namespace, pod, process_name) so series stay stable across
// scrapes — pid is deliberately not a label (it would churn a new series on
// every restart). Processes that couldn't be attributed to a pod report empty
// namespace/pod, so they still count toward the GPU's total.
func writeMetrics(w io.Writer, node string, procs []process, devs []device) {
	type key struct {
		gpu                  int
		uuid, namespace, pod string
		process              string
	}
	sums := map[key]int64{}
	for _, p := range procs {
		k := key{gpu: p.GPUIndex, uuid: p.GPUUUID, namespace: p.Namespace, pod: p.Pod, process: p.ProcessName}
		sums[k] += p.UsedBytes
	}
	keys := make([]key, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	// Deterministic output makes a manual `curl :9401/metrics` readable; Prom
	// itself doesn't care about ordering.
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.namespace != b.namespace {
			return a.namespace < b.namespace
		}
		if a.pod != b.pod {
			return a.pod < b.pod
		}
		if a.gpu != b.gpu {
			return a.gpu < b.gpu
		}
		return a.process < b.process
	})

	fmt.Fprintln(w, "# HELP vram_used_bytes GPU memory used by a pod on a GPU, in bytes (summed over the pod's processes of the same name).")
	fmt.Fprintln(w, "# TYPE vram_used_bytes gauge")
	for _, k := range keys {
		fmt.Fprintf(w, "vram_used_bytes{node=\"%s\",gpu=\"%d\",gpu_uuid=\"%s\",namespace=\"%s\",pod=\"%s\",process_name=\"%s\"} %d\n",
			esc(node), k.gpu, esc(k.uuid), esc(k.namespace), esc(k.pod), esc(k.process), sums[k])
	}

	fmt.Fprintln(w, "# HELP gpu_mem_used_bytes Device framebuffer memory used, in bytes (0 on unified-memory GB10).")
	fmt.Fprintln(w, "# TYPE gpu_mem_used_bytes gauge")
	for _, d := range devs {
		fmt.Fprintf(w, "gpu_mem_used_bytes{node=\"%s\",gpu=\"%d\",gpu_uuid=\"%s\"} %d\n", esc(node), d.Index, esc(d.UUID), d.MemUsedBytes)
	}

	fmt.Fprintln(w, "# HELP gpu_mem_total_bytes Device framebuffer memory total, in bytes (0 on unified-memory GB10).")
	fmt.Fprintln(w, "# TYPE gpu_mem_total_bytes gauge")
	for _, d := range devs {
		fmt.Fprintf(w, "gpu_mem_total_bytes{node=\"%s\",gpu=\"%d\",gpu_uuid=\"%s\"} %d\n", esc(node), d.Index, esc(d.UUID), d.MemTotalBytes)
	}

	fmt.Fprintln(w, "# HELP gpu_util_percent Device-level SM utilization, percent (0 where unavailable).")
	fmt.Fprintln(w, "# TYPE gpu_util_percent gauge")
	for _, d := range devs {
		fmt.Fprintf(w, "gpu_util_percent{node=\"%s\",gpu=\"%d\",gpu_uuid=\"%s\"} %d\n", esc(node), d.Index, esc(d.UUID), d.UtilPercent)
	}
}

// esc escapes a Prometheus label value: backslash, double-quote and newline,
// per the exposition format. Returns the inner string (callers add the quotes).
func esc(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// --- nvidia-smi -------------------------------------------------------------

type computeApp struct {
	pid     int
	usedMiB int64
	uuid    string
	name    string
}

func nvidiaSMI() string { return envDefault("NVIDIA_SMI", "nvidia-smi") }

// computeApps runs the per-process query. Fields are explicitly ordered so the
// CSV columns are positional and stable.
func computeApps(ctx context.Context) ([]computeApp, error) {
	out, err := exec.CommandContext(ctx, nvidiaSMI(),
		"--query-compute-apps=pid,used_memory,gpu_uuid,process_name",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi compute-apps: %w", err)
	}
	var apps []computeApp
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := splitCSV(line)
		if len(cols) < 4 {
			continue
		}
		pid, err := strconv.Atoi(cols[0])
		if err != nil {
			continue
		}
		used, _ := strconv.ParseInt(strings.TrimSpace(cols[1]), 10, 64)
		apps = append(apps, computeApp{
			pid: pid, usedMiB: used,
			uuid: strings.TrimSpace(cols[2]),
			name: strings.TrimSpace(cols[3]),
		})
	}
	return apps, nil
}

// gpuIndexByUUID maps each GPU's UUID to its nvidia-smi index. Best-effort:
// an error yields an empty map (processes then report GPUIndex -1).
func gpuIndexByUUID(ctx context.Context) map[string]int {
	out := map[string]int{}
	b, err := exec.CommandContext(ctx, nvidiaSMI(),
		"--query-gpu=index,uuid", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		cols := splitCSV(line)
		if len(cols) < 2 {
			continue
		}
		if idx, err := strconv.Atoi(strings.TrimSpace(cols[0])); err == nil {
			out[strings.TrimSpace(cols[1])] = idx
		}
	}
	return out
}

// devices reports per-GPU device-level utilization. Works on GeForce and the
// GB10 (unlike per-process util). Best-effort: an error yields no devices.
func devices(ctx context.Context) []device {
	b, err := exec.CommandContext(ctx, nvidiaSMI(),
		"--query-gpu=index,uuid,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var out []device
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		cols := splitCSV(line)
		if len(cols) < 5 {
			continue
		}
		idx, err := strconv.Atoi(cols[0])
		if err != nil {
			continue
		}
		// util / memory are "[N/A]" on unified configs — Atoi fails → 0.
		util, _ := strconv.Atoi(cols[2])
		usedMiB, _ := strconv.ParseInt(cols[3], 10, 64)
		totalMiB, _ := strconv.ParseInt(cols[4], 10, 64)
		out = append(out, device{
			Index: idx, UUID: cols[1], UtilPercent: util,
			MemUsedBytes:  usedMiB * mib,
			MemTotalBytes: totalMiB * mib,
		})
	}
	return out
}

func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// --- cgroup → pod identity --------------------------------------------------

// podRefForPID extracts the pod UID and container id from a process's cgroup.
// Works for both cgroup v1 and v2 kubepods layouts; either may be empty if the
// process isn't in a pod (e.g. a host process), in which case it stays
// unattributed.
func podRefForPID(pid int) (uid, containerID string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// Take the cgroup path (after the last ':').
		path := line
		if i := strings.LastIndex(line, ":"); i >= 0 {
			path = line[i+1:]
		}
		if !strings.Contains(path, "pod") && !strings.Contains(path, "kubepods") {
			continue
		}
		for _, seg := range strings.Split(path, "/") {
			if u := podUIDFromSegment(seg); u != "" {
				uid = u
			}
			if c := containerIDFromSegment(seg); c != "" {
				containerID = c
			}
		}
		if uid != "" || containerID != "" {
			return uid, containerID
		}
	}
	return uid, containerID
}

// podUIDFromSegment pulls the pod UID out of a cgroup segment like
// "pod<uid>.slice", "kubepods-besteffort-pod<uid>.slice" or "pod<uid>",
// normalizing systemd's underscores back to the dashed form k8s reports.
func podUIDFromSegment(seg string) string {
	// LastIndex, not Index: "kubepods-burstable-pod<uid>" contains "pod" twice
	// and we want the one introducing the UID, not the one in "kubepods".
	i := strings.LastIndex(seg, "pod")
	if i < 0 {
		return ""
	}
	rest := seg[i+3:]
	rest = strings.TrimSuffix(rest, ".slice")
	rest = strings.TrimSuffix(rest, ".scope")
	if rest == "" {
		return ""
	}
	norm := strings.ReplaceAll(rest, "_", "-")
	// A UID is 32 hex digits with dashes; a quick sanity gate avoids matching
	// stray "pod"-prefixed names.
	if len(norm) < 32 {
		return ""
	}
	return strings.ToLower(norm)
}

// containerIDFromSegment pulls the container id out of the final cgroup
// segment, stripping the runtime prefix and ".scope" systemd suffix.
func containerIDFromSegment(seg string) string {
	s := strings.TrimSuffix(seg, ".scope")
	for _, p := range []string{"cri-containerd-", "crio-", "docker-", "containerd-"} {
		s = strings.TrimPrefix(s, p)
	}
	// Container ids are 64 hex chars; anything shorter isn't one.
	if len(s) < 64 {
		return ""
	}
	return strings.ToLower(s[:64])
}

// --- kubernetes -------------------------------------------------------------

type podRef struct {
	name, namespace string
}

func podIndexes(pods map[string]podMeta) (byUID, byContainer map[string]podRef) {
	byUID, byContainer = map[string]podRef{}, map[string]podRef{}
	for _, p := range pods {
		ref := podRef{name: p.name, namespace: p.namespace}
		if p.uid != "" {
			byUID[strings.ToLower(p.uid)] = ref
		}
		for _, cid := range p.containerIDs {
			byContainer[cid] = ref
		}
	}
	return byUID, byContainer
}

type podMeta struct {
	name, namespace, uid string
	containerIDs         []string
}

type k8sClient struct {
	apiBase   string
	tokenPath string
	client    *http.Client
}

func newK8s() (*k8sClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not in a cluster: KUBERNETES_SERVICE_HOST/PORT unset")
	}
	caPEM, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read serviceaccount ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("serviceaccount ca.crt: no certs parsed")
	}
	return &k8sClient{
		apiBase:   "https://" + net.JoinHostPort(host, port),
		tokenPath: saDir + "/token",
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}, nil
}

// podsOnNode lists pods scheduled on this node (all namespaces) and reduces
// them to the identity fields the cgroup join needs.
func (k *k8sClient) podsOnNode(ctx context.Context, node string) (map[string]podMeta, error) {
	path := "/api/v1/pods?fieldSelector=spec.nodeName=" + node
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	tok, err := os.ReadFile(k.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read serviceaccount token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				UID       string `json:"uid"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					ContainerID string `json:"containerID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	out := make(map[string]podMeta, len(list.Items))
	for _, it := range list.Items {
		pm := podMeta{
			name: it.Metadata.Name, namespace: it.Metadata.Namespace, uid: it.Metadata.UID,
		}
		for _, cs := range it.Status.ContainerStatuses {
			// "containerd://<64-hex>" → "<64-hex>"
			id := cs.ContainerID
			if i := strings.Index(id, "://"); i >= 0 {
				id = id[i+3:]
			}
			if len(id) >= 64 {
				pm.containerIDs = append(pm.containerIDs, strings.ToLower(id[:64]))
			}
		}
		out[it.Metadata.UID] = pm
	}
	return out, nil
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
