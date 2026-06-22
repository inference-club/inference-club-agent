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
	st := k.assembleState(nil, nil, nodes, nil, nil, gpuByNode, false)
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
