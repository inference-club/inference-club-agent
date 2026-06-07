package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
)

// authCapture is an upstream that records the Authorization header it received.
func authCapture(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func chatReq(t *testing.T, model string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`"}`))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// When a service declares api_key, the router must authenticate to its backend
// with that key — overriding whatever inbound Authorization arrived.
func TestRouter_InjectsConfiguredAPIKey(t *testing.T) {
	srv, got := authCapture(t)
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig",
			Services: []manifest.Service{{
				Name:   "lmstudio",
				Engine: "lmstudio",
				URL:    srv.URL + "/v1",
				APIKey: "sk-lm-secret:abc",
				Models: []manifest.Model{{ID: "gemma"}},
			}},
		}},
	}
	fb, _ := url.Parse("http://127.0.0.1:1/v1")
	rr := New(m, fb)

	req := chatReq(t, "gemma")
	req.Header.Set("Authorization", "Bearer consumer-inbound-key") // must be overridden
	rr.ServeHTTP(httptest.NewRecorder(), req)

	if want := "Bearer sk-lm-secret:abc"; *got != want {
		t.Fatalf("upstream Authorization = %q, want %q", *got, want)
	}
}

// When a service has no api_key, the router must not inject one — the inbound
// Authorization (if any) is forwarded untouched.
func TestRouter_NoAPIKeyLeavesInboundAuth(t *testing.T) {
	srv, got := authCapture(t)
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig",
			Services: []manifest.Service{{
				Name:   "vllm",
				Engine: "vllm",
				URL:    srv.URL + "/v1",
				Models: []manifest.Model{{ID: "qwen"}},
			}},
		}},
	}
	fb, _ := url.Parse("http://127.0.0.1:1/v1")
	rr := New(m, fb)

	req := chatReq(t, "qwen")
	req.Header.Set("Authorization", "Bearer inbound")
	rr.ServeHTTP(httptest.NewRecorder(), req)

	if *got != "Bearer inbound" {
		t.Fatalf("upstream Authorization = %q, want inbound to be preserved", *got)
	}
}
