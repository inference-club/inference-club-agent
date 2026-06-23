package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A trimmed two-GPU dcgm-exporter body (real label sets, irrelevant metrics
// dropped). gpu 0: 16234/24080 MiB used, 55% util. gpu 1: 1000/24080, 0%.
const dcgmFixture = `# HELP DCGM_FI_DEV_GPU_UTIL GPU utilization (in %).
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-aaa",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 55
DCGM_FI_DEV_GPU_UTIL{gpu="1",UUID="GPU-bbb",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 0
DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-aaa",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 7846
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-aaa",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 16234
DCGM_FI_DEV_FB_FREE{gpu="1",UUID="GPU-bbb",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 23080
DCGM_FI_DEV_FB_USED{gpu="1",UUID="GPU-bbb",modelName="NVIDIA GeForce RTX 4090",Hostname="dcgm-x"} 1000
DCGM_FI_PROF_GR_ENGINE_ACTIVE{gpu="0"} 0.42
`

func TestParseDCGM(t *testing.T) {
	g, err := parseDCGM(strings.NewReader(dcgmFixture))
	if err != nil {
		t.Fatalf("parseDCGM: %v", err)
	}
	if g == nil {
		t.Fatal("parseDCGM returned nil for a GPU-bearing body")
	}
	if len(g.Devices) != 2 {
		t.Fatalf("want 2 devices, got %d: %+v", len(g.Devices), g.Devices)
	}

	d0 := g.Devices[0]
	if d0.Index != 0 || d0.Model != "NVIDIA GeForce RTX 4090" {
		t.Errorf("device 0 = %+v", d0)
	}
	if d0.VRAMUsedBytes != 16234*mib || d0.VRAMTotalBytes != (16234+7846)*mib || d0.UtilPercent != 55 {
		t.Errorf("device 0 stats = %+v", d0)
	}
	d1 := g.Devices[1]
	if d1.Index != 1 || d1.VRAMUsedBytes != 1000*mib || d1.UtilPercent != 0 {
		t.Errorf("device 1 = %+v", d1)
	}

	if g.VRAMUsedBytes != (16234+1000)*mib {
		t.Errorf("node used = %d", g.VRAMUsedBytes)
	}
	if g.VRAMTotalBytes != (24080+24080)*mib {
		t.Errorf("node total = %d", g.VRAMTotalBytes)
	}
	if g.UtilPercent != 27 { // (55+0)/2 truncated
		t.Errorf("node util = %d, want 27", g.UtilPercent)
	}
}

// A reachable exporter with no GPU lines must yield nil, not an empty card.
func TestParseDCGMNoGPUs(t *testing.T) {
	g, err := parseDCGM(strings.NewReader("# HELP something\nsome_other_metric 1\n"))
	if err != nil {
		t.Fatalf("parseDCGM: %v", err)
	}
	if g != nil {
		t.Errorf("want nil for GPU-less body, got %+v", g)
	}
}

// scrapeNodeGPUs walks node InternalIPs and attaches stats. Here both fixture
// nodes point at one dcgm server; the no-address node is skipped.
func TestScrapeNodeGPUs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dcgmFixture))
	}))
	defer srv.Close()
	// srv.URL is http://127.0.0.1:PORT — reuse its host:port as the node IP/port.
	host, port := splitHostPort(t, srv.URL)

	k := &Kubernetes{DCGMPort: port}
	nodes := []k8sNode{
		nodeWithIP("a1", host),
		nodeWithIP("a3", host),
		nodeWithIP("a2", ""), // parked: no InternalIP → skipped
	}
	got := k.scrapeNodeGPUs(context.Background(), nodes)
	if len(got) != 2 {
		t.Fatalf("want 2 scraped nodes, got %d: %+v", len(got), got)
	}
	if got["a1"] == nil || got["a1"].VRAMUsedBytes != (16234+1000)*mib {
		t.Errorf("a1 gpu = %+v", got["a1"])
	}
	if _, ok := got["a2"]; ok {
		t.Error("a2 has no InternalIP and must be absent")
	}
}

// DCGMPort<=0 disables scraping entirely (the test-friendly zero value).
func TestScrapeNodeGPUsDisabled(t *testing.T) {
	k := &Kubernetes{DCGMPort: 0}
	got := k.scrapeNodeGPUs(context.Background(), []k8sNode{nodeWithIP("a1", "10.0.0.1")})
	if len(got) != 0 {
		t.Errorf("scraping must be off when DCGMPort<=0, got %+v", got)
	}
}

// assembleState copies the per-node GPU map onto the matching node and leaves
// the rest nil.
func TestAssembleStateAttachesGPU(t *testing.T) {
	k := &Kubernetes{Namespace: "inference-club"}
	nodes := []k8sNode{nodeWithIP("a1", "10.0.0.1"), nodeWithIP("a2", "10.0.0.2")}
	gpuByNode := map[string]*NodeGPU{
		"a1": {VRAMUsedBytes: 16234 * mib, VRAMTotalBytes: 24080 * mib, UtilPercent: 55},
	}
	st := k.assembleState(nil, nil, nodes, nil, nil, gpuByNode, nil, false)
	if len(st.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(st.Nodes))
	}
	var a1, a2 *NodeState
	for i := range st.Nodes {
		switch st.Nodes[i].Name {
		case "a1":
			a1 = &st.Nodes[i]
		case "a2":
			a2 = &st.Nodes[i]
		}
	}
	if a1 == nil || a1.GPU == nil || a1.GPU.VRAMUsedBytes != 16234*mib {
		t.Errorf("a1 GPU = %+v", a1)
	}
	if a2 == nil || a2.GPU != nil {
		t.Errorf("a2 must have nil GPU, got %+v", a2)
	}
}

// assembleState attributes per-process VRAM to the managed service backing the
// pod, and sums it per pod — including when a GPU has no dcgm totals (the GPU
// is synthesized so the processes still surface).
func TestAssembleStateAttributesVRAMToService(t *testing.T) {
	k := &Kubernetes{Namespace: "inference-club"}
	nodes := []k8sNode{nodeWithIP("a1", "10.0.0.1")}

	var svc k8sService
	svc.Metadata.Name = "vllm-main"
	svc.Spec.Selector = map[string]string{"app": "vllm"}

	var pod k8sPod
	pod.Metadata.Name = "vllm-main-abc"
	pod.Metadata.Labels = map[string]string{"app": "vllm"}
	pod.Spec.NodeName = "a1"
	pod.Status.Phase = "Running"

	vramByNode := map[string]*nodeVRAM{
		"a1": {Processes: []GPUProcess{
			{PID: 101, GPUIndex: 0, GPUUUID: "GPU-aaa", UsedBytes: 8 * 1024 * mib, Pod: "vllm-main-abc", Namespace: "inference-club"},
			{PID: 102, GPUIndex: 0, GPUUUID: "GPU-aaa", UsedBytes: 2 * 1024 * mib, Pod: "vllm-main-abc", Namespace: "inference-club"},
		}},
	}

	// gpuByNode nil → the node has no dcgm totals; the reporter still drives a
	// synthesized GPU carrying the per-process breakdown.
	st := k.assembleState([]k8sService{svc}, []k8sPod{pod}, nodes, nil, nil, nil, vramByNode, false)

	if len(st.Nodes) != 1 || st.Nodes[0].GPU == nil {
		t.Fatalf("want node a1 with a synthesized GPU, got %+v", st.Nodes)
	}
	procs := st.Nodes[0].GPU.Processes
	if len(procs) != 2 {
		t.Fatalf("want 2 processes, got %d", len(procs))
	}
	for _, p := range procs {
		if p.Service != "vllm-main" {
			t.Errorf("pid %d not attributed to its service: %+v", p.PID, p)
		}
	}
	if len(st.Pods) != 1 {
		t.Fatalf("want 1 pod, got %d", len(st.Pods))
	}
	if want := int64(10 * 1024 * mib); st.Pods[0].GPUVRAMUsedBytes != want {
		t.Errorf("pod vram = %d, want %d", st.Pods[0].GPUVRAMUsedBytes, want)
	}
}

// On a unified-memory node (no dcgm framebuffer total) the reporter's
// per-process VRAM drives a synthesized GPU: used = sum of processes, total =
// the node's unified memory pool, flagged unified.
func TestAssembleStateUnifiedMemoryNode(t *testing.T) {
	k := &Kubernetes{Namespace: "inference-club"}
	spark := nodeWithIP("spark", "10.0.0.9")
	spark.Status.Capacity = map[string]string{"memory": "119Gi"}

	var svc k8sService
	svc.Metadata.Name = "lmstudio"
	svc.Spec.Selector = map[string]string{"app": "lmstudio"}
	var pod k8sPod
	pod.Metadata.Name = "lmstudio-xyz"
	pod.Metadata.Labels = map[string]string{"app": "lmstudio"}
	pod.Spec.NodeName = "spark"
	pod.Status.Phase = "Running"

	vramByNode := map[string]*nodeVRAM{
		"spark": {
			Processes:   []GPUProcess{{PID: 7, UsedBytes: 41 * 1024 * mib, Pod: "lmstudio-xyz"}},
			UtilPercent: 73,
		},
	}
	// gpuByNode nil → spark has no dcgm; the reporter alone drives the GPU.
	st := k.assembleState([]k8sService{svc}, []k8sPod{pod}, []k8sNode{spark}, nil, nil, nil, vramByNode, false)

	g := st.Nodes[0].GPU
	if g == nil || !g.Unified {
		t.Fatalf("spark GPU must be flagged unified: %+v", g)
	}
	if g.UtilPercent != 73 {
		t.Errorf("unified util = %d, want reporter util 73", g.UtilPercent)
	}
	if g.VRAMUsedBytes != 41*1024*mib {
		t.Errorf("unified used = %d, want %d", g.VRAMUsedBytes, 41*1024*mib)
	}
	if g.VRAMTotalBytes != 119*(1<<30) {
		t.Errorf("unified total = %d, want node memory %d", g.VRAMTotalBytes, int64(119)*(1<<30))
	}
	if g.Processes[0].Service != "lmstudio" {
		t.Errorf("process not attributed: %+v", g.Processes[0])
	}
}

// A discrete GPU node without dcgm (a2): the reporter supplies a real
// framebuffer total via nvidia-smi, so the node gets a proper VRAM bar and is
// NOT mislabeled unified.
func TestAssembleStateDiscreteNodeWithoutDcgm(t *testing.T) {
	k := &Kubernetes{Namespace: "inference-club"}
	a2 := nodeWithIP("a2", "10.0.0.2")
	a2.Status.Capacity = map[string]string{"memory": "62Gi"} // node RAM, must NOT be used as VRAM total

	var svc k8sService
	svc.Metadata.Name = "flux2-klein"
	svc.Spec.Selector = map[string]string{"app": "flux2"}
	var pod k8sPod
	pod.Metadata.Name = "flux2-klein-abc"
	pod.Metadata.Labels = map[string]string{"app": "flux2"}
	pod.Spec.NodeName = "a2"
	pod.Status.Phase = "Running"

	vramByNode := map[string]*nodeVRAM{
		"a2": {
			Processes:     []GPUProcess{{PID: 9, UsedBytes: 15 * 1024 * mib, Pod: "flux2-klein-abc"}},
			UtilPercent:   30,
			MemUsedBytes:  16 * 1024 * mib,
			MemTotalBytes: 24 * 1024 * mib, // real 4090 framebuffer
		},
	}
	st := k.assembleState([]k8sService{svc}, []k8sPod{pod}, []k8sNode{a2}, nil, nil, nil, vramByNode, false)

	g := st.Nodes[0].GPU
	if g == nil || g.Unified {
		t.Fatalf("a2 must be a discrete (non-unified) GPU: %+v", g)
	}
	if g.VRAMTotalBytes != 24*1024*mib || g.VRAMUsedBytes != 16*1024*mib {
		t.Errorf("a2 VRAM = %d/%d, want reporter framebuffer 16/24 GiB", g.VRAMUsedBytes, g.VRAMTotalBytes)
	}
	if g.UtilPercent != 30 {
		t.Errorf("a2 util = %d, want 30", g.UtilPercent)
	}
}

// --- helpers ----------------------------------------------------------------

func nodeWithIP(name, ip string) k8sNode {
	var n k8sNode
	n.Metadata.Name = name
	if ip != "" {
		n.Status.Addresses = append(n.Status.Addresses, struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		}{Type: "InternalIP", Address: ip})
	}
	return n
}

func splitHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	trimmed := strings.TrimPrefix(rawURL, "http://")
	host, portStr, found := strings.Cut(trimmed, ":")
	if !found {
		t.Fatalf("no port in %q", rawURL)
	}
	port := 0
	for _, r := range portStr {
		port = port*10 + int(r-'0')
	}
	return host, port
}
