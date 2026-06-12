package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fixtureAPI serves just enough of the Kubernetes REST API for Build():
// one GPU-backed Service (magpie-tts on node a1), one selector-less external
// Service (lmstudio at 192.168.6.19) with an api-key Secret, and one Service
// with no backing pod (must be dropped).
func fixtureAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(pattern, body string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
	}

	reply("/api/v1/namespaces/inference-club/services", `{"items":[
	  {"metadata":{"name":"magpie-tts",
	    "labels":{"inference-club.com/managed":"true","inference-club.com/type":"tts","inference-club.com/engine":"other"},
	    "annotations":{"inference-club.com/base-path":"/v1",
	      "inference-club.com/models":"- id: magpie-tts-multilingual\n"}},
	   "spec":{"selector":{"app":"magpie-tts"},"ports":[{"name":"http","port":9000}]}},
	  {"metadata":{"name":"lmstudio",
	    "labels":{"inference-club.com/managed":"true","inference-club.com/type":"llm","inference-club.com/engine":"lmstudio"},
	    "annotations":{"inference-club.com/base-path":"/v1",
	      "inference-club.com/api-key-secret":"lmstudio-key",
	      "inference-club.com/features":"timestamps, vision",
	      "inference-club.com/models":"- id: google/gemma-4-12b\n  hf: google/gemma-4-12B\n  input_modalities: [text, image, audio]\n"}},
	   "spec":{"ports":[{"port":1234}]}},
	  {"metadata":{"name":"ghost","labels":{"inference-club.com/managed":"true"}},
	   "spec":{"selector":{"app":"ghost"},"ports":[{"port":1}]}}
	]}`)

	reply("/api/v1/namespaces/inference-club/pods", `{"items":[
	  {"metadata":{"name":"magpie-tts-abc","labels":{"app":"magpie-tts"}},
	   "spec":{"nodeName":"a1","containers":[
	     {"image":"nvcr.io/nim/nvidia/magpie-tts-multilingual:latest",
	      "command":["/opt/nim/start.sh"],"args":["--log-level","info"]}]},
	   "status":{"phase":"Running","containerStatuses":[
	     {"name":"magpie","restartCount":2,"ready":true,"state":{"running":{}}}]}},
	  {"metadata":{"name":"magpie-tts-new","labels":{"app":"magpie-tts"}},
	   "spec":{"nodeName":"a1","containers":[
	     {"image":"nvcr.io/nim/nvidia/magpie-tts-multilingual:next"}]},
	   "status":{"phase":"Pending","containerStatuses":[
	     {"name":"magpie","restartCount":0,"ready":false,
	      "state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}
	]}`)

	reply("/apis/discovery.k8s.io/v1/namespaces/inference-club/endpointslices", `{"items":[
	  {"metadata":{"name":"lmstudio-1","labels":{"kubernetes.io/service-name":"lmstudio"}},
	   "endpoints":[{"addresses":["192.168.6.19"]}]}
	]}`)

	reply("/api/v1/nodes", `{"items":[
	  {"metadata":{"name":"a1","labels":{"inference-club.com/box":"a1",
	     "nvidia.com/gpu.product":"NVIDIA-GeForce-RTX-4090","nvidia.com/gpu.memory":"24564"}},
	   "status":{"allocatable":{"nvidia.com/gpu":"1","memory":"65536000Ki"},
	     "capacity":{"memory":"65929340Ki"},
	     "addresses":[{"type":"InternalIP","address":"192.168.5.253"}],
	     "conditions":[{"type":"MemoryPressure","status":"False"},
	       {"type":"Ready","status":"True"}],
	     "nodeInfo":{"architecture":"amd64","kubeletVersion":"v1.33.1+k3s1",
	       "osImage":"Ubuntu 24.04.2 LTS"}}},
	  {"metadata":{"name":"spark","labels":{"inference-club.com/box":"spark"}},
	   "status":{"allocatable":{"memory":"119Gi"},"capacity":{"memory":"120Gi"},
	     "addresses":[{"type":"InternalIP","address":"192.168.5.250"}],
	     "conditions":[{"type":"Ready","status":"False","reason":"KubeletNotReady"}],
	     "nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.33.1+k3s1",
	       "osImage":"Ubuntu 24.04.2 LTS"}}}
	]}`)

	reply("/apis/metrics.k8s.io/v1beta1/nodes", `{"items":[
	  {"metadata":{"name":"a1"},"usage":{"cpu":"2","memory":"32768000Ki"}}
	]}`)
	reply("/apis/metrics.k8s.io/v1beta1/namespaces/inference-club/pods", `{"items":[
	  {"metadata":{"name":"magpie-tts-abc"},
	   "containers":[{"usage":{"memory":"4Gi"}},{"usage":{"memory":"1Gi"}}]}
	]}`)

	reply("/api/v1/namespaces/inference-club/secrets/lmstudio-key",
		`{"data":{"api-key":"`+base64.StdEncoding.EncodeToString([]byte("sk-local-test\n"))+`"}}`)

	return httptest.NewServer(mux)
}

func TestBuildFromCluster(t *testing.T) {
	srv := fixtureAPI(t)
	defer srv.Close()

	k := &Kubernetes{AgentName: "club-host-k8s", Namespace: "inference-club", APIBase: srv.URL}
	lr, verrs, err := k.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if verrs != nil {
		t.Fatalf("built manifest failed validation:\n  %s", strings.Join(verrs, "\n  "))
	}
	m := lr.Manifest

	if m.Agent.Name != "club-host-k8s" {
		t.Errorf("agent name = %q", m.Agent.Name)
	}
	if len(m.Hosts) != 2 {
		t.Fatalf("want 2 hosts (a1 + external-lmstudio), got %d: %+v", len(m.Hosts), m.Hosts)
	}

	// Hosts are sorted by id: "a1" < "external-lmstudio".
	a1 := m.Hosts[0]
	if a1.ID != "a1" || a1.Address != "192.168.5.253" || a1.Hostname != "a1" {
		t.Errorf("a1 host = %+v", a1)
	}
	if a1.GPU == nil || a1.GPU.Vendor != "nvidia" || a1.GPU.Count != 1 ||
		a1.GPU.Model != "NVIDIA-GeForce-RTX-4090" || a1.GPU.VRAMGB < 23.9 || a1.GPU.VRAMGB > 24.0 {
		t.Errorf("a1 gpu = %+v", a1.GPU)
	}
	if len(a1.Services) != 1 {
		t.Fatalf("a1 services = %+v", a1.Services)
	}
	tts := a1.Services[0]
	if tts.Name != "magpie-tts" || tts.Type != "tts" || tts.Engine != "other" {
		t.Errorf("magpie service = %+v", tts)
	}
	if tts.URL != "http://magpie-tts.inference-club.svc.cluster.local:9000/v1" {
		t.Errorf("magpie url = %q", tts.URL)
	}
	wantCmd := "nvcr.io/nim/nvidia/magpie-tts-multilingual:latest /opt/nim/start.sh --log-level info"
	if tts.Command != wantCmd {
		t.Errorf("magpie command = %q, want %q", tts.Command, wantCmd)
	}
	if len(tts.Models) != 1 || tts.Models[0].ID != "magpie-tts-multilingual" {
		t.Errorf("magpie models = %+v", tts.Models)
	}

	ext := m.Hosts[1]
	if ext.ID != "external-lmstudio" || ext.Address != "192.168.6.19" || ext.GPU != nil {
		t.Errorf("external host = %+v", ext)
	}
	if len(ext.Services) != 1 {
		t.Fatalf("external services = %+v", ext.Services)
	}
	llm := ext.Services[0]
	if llm.URL != "http://lmstudio.inference-club.svc.cluster.local:1234/v1" {
		t.Errorf("lmstudio url = %q", llm.URL)
	}
	if llm.APIKey != "sk-local-test" {
		t.Errorf("lmstudio api key = %q (want trimmed secret value)", llm.APIKey)
	}
	if !reflect.DeepEqual(llm.Features, []string{"timestamps", "vision"}) {
		t.Errorf("lmstudio features = %+v", llm.Features)
	}
	if len(llm.Models) != 1 || llm.Models[0].Hf != "google/gemma-4-12B" ||
		!reflect.DeepEqual(llm.Models[0].InputModalities, []string{"text", "image", "audio"}) {
		t.Errorf("lmstudio models = %+v", llm.Models)
	}

	// "ghost" had no backing pod → must not appear anywhere.
	for _, h := range m.Hosts {
		for _, s := range h.Services {
			if s.Name == "ghost" {
				t.Error("ghost service leaked into the manifest")
			}
		}
	}

	// The secret must never reach the upload payload.
	raw, parsed, err := m.RedactedForUpload()
	if err != nil {
		t.Fatalf("RedactedForUpload: %v", err)
	}
	if strings.Contains(string(raw), "sk-local-test") {
		t.Error("api key leaked into raw upload YAML")
	}
	b, _ := json.Marshal(parsed)
	if strings.Contains(string(b), "sk-local-test") {
		t.Error("api key leaked into parsed upload JSON")
	}
}

// TestBuildDeterminism: two Builds of the same cluster state must produce
// byte-identical Raw YAML — the poll loop diffs bytes to decide re-push.
func TestBuildDeterminism(t *testing.T) {
	srv := fixtureAPI(t)
	defer srv.Close()
	k := &Kubernetes{AgentName: "x", Namespace: "inference-club", APIBase: srv.URL}
	a, _, err := k.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := k.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Raw) != string(b.Raw) {
		t.Error("two identical Builds produced different YAML")
	}
}
