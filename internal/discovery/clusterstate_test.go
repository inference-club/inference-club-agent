package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClusterState(t *testing.T) {
	srv := fixtureAPI(t)
	defer srv.Close()

	k := &Kubernetes{AgentName: "club-host-k8s", Namespace: "inference-club", APIBase: srv.URL}
	st, err := k.ClusterState(context.Background())
	if err != nil {
		t.Fatalf("ClusterState: %v", err)
	}

	if st.Discovery != "kubernetes" {
		t.Errorf("discovery = %q", st.Discovery)
	}
	if !st.MetricsAvailable {
		t.Error("metrics_available = false, fixture serves metrics.k8s.io")
	}

	if len(st.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d: %+v", len(st.Nodes), st.Nodes)
	}
	a1 := st.Nodes[0]
	if a1.Name != "a1" || a1.HostID != "a1" || !a1.Ready {
		t.Errorf("a1 = %+v", a1)
	}
	if a1.Architecture != "amd64" || a1.KubeletVersion != "v1.33.1+k3s1" {
		t.Errorf("a1 nodeInfo = %+v", a1)
	}
	if a1.GPUAllocatable != 1 {
		t.Errorf("a1 gpu_allocatable = %d", a1.GPUAllocatable)
	}
	if a1.Memory.AllocatableBytes != 65536000*1024 ||
		a1.Memory.CapacityBytes != 65929340*1024 ||
		a1.Memory.UsageBytes != 32768000*1024 {
		t.Errorf("a1 memory = %+v", a1.Memory)
	}
	if len(a1.Conditions) != 2 {
		t.Errorf("a1 conditions = %+v", a1.Conditions)
	}

	spark := st.Nodes[1]
	if spark.Name != "spark" || spark.Ready {
		t.Errorf("spark must be NotReady: %+v", spark)
	}
	if spark.Memory.AllocatableBytes != 119<<30 {
		t.Errorf("spark allocatable = %d", spark.Memory.AllocatableBytes)
	}

	// Both magpie pods report — the Pending ImagePullBackOff one is exactly
	// the degradation the viz must show. The ghost service has no pods.
	if len(st.Pods) != 2 {
		t.Fatalf("want 2 pods, got %d: %+v", len(st.Pods), st.Pods)
	}
	running := st.Pods[0]
	if running.Name != "magpie-tts-abc" || running.Service != "magpie-tts" ||
		running.Node != "a1" || running.HostID != "a1" {
		t.Errorf("running pod = %+v", running)
	}
	if running.Phase != "Running" || !running.Ready || running.Restarts != 2 {
		t.Errorf("running pod state = %+v", running)
	}
	if running.MemoryUsageBytes != 5<<30 { // 4Gi + 1Gi across containers
		t.Errorf("running pod memory = %d", running.MemoryUsageBytes)
	}
	pending := st.Pods[1]
	if pending.Name != "magpie-tts-new" || pending.Phase != "Pending" ||
		pending.Ready || pending.Reason != "ImagePullBackOff" {
		t.Errorf("pending pod = %+v", pending)
	}
}

// Metrics-server absent (404 on metrics.k8s.io) must not fail the snapshot —
// it degrades to allocatable-only with metrics_available=false.
func TestClusterStateWithoutMetrics(t *testing.T) {
	upstream := fixtureAPI(t)
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/apis/metrics.k8s.io") {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(upstream.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	k := &Kubernetes{AgentName: "x", Namespace: "inference-club", APIBase: proxy.URL}
	st, err := k.ClusterState(context.Background())
	if err != nil {
		t.Fatalf("ClusterState: %v", err)
	}
	if st.MetricsAvailable {
		t.Error("metrics_available = true without metrics-server")
	}
	if len(st.Nodes) != 2 || st.Nodes[0].Memory.UsageBytes != 0 {
		t.Errorf("nodes = %+v", st.Nodes)
	}
}

func TestParseQuantityBytes(t *testing.T) {
	cases := map[string]int64{
		"":           0,
		"1073741824": 1073741824,
		"65536000Ki": 65536000 * 1024,
		"2Gi":        2 << 30,
		"1.5Gi":      3 << 29,
		"129M":       129e6,
		"bogus":      0,
		"-5Gi":       0,
	}
	for in, want := range cases {
		if got := parseQuantityBytes(in); got != want {
			t.Errorf("parseQuantityBytes(%q) = %d, want %d", in, got, want)
		}
	}
}
