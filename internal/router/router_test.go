package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
)

// fakeUpstream stands in for an OpenAI-compatible service. Records every
// request so tests can assert which upstream the router picked.
type fakeUpstream struct {
	name   string
	server *httptest.Server
	calls  []recordedCall
}

type recordedCall struct {
	method string
	path   string
	body   string
}

func newFakeUpstream(t *testing.T, name string) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{name: name}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.calls = append(u.calls, recordedCall{
			method: r.Method,
			path:   r.URL.Path,
			body:   string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"upstream":%q,"path":%q}`, name, r.URL.Path)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *fakeUpstream) URL() *url.URL {
	parsed, _ := url.Parse(u.server.URL + "/v1")
	return parsed
}

// twoBackendManifest builds a manifest where service A serves model-a
// and service B serves model-b. Used by most routing tests.
func twoBackendManifest(aURL, bURL string) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{
			{
				ID: "rig-a",
				Services: []manifest.Service{{
					Name:   "vllm-a",
					Engine: "vllm",
					URL:    aURL,
					Models: []manifest.Model{{ID: "model-a"}},
				}},
			},
			{
				ID: "rig-b",
				Services: []manifest.Service{{
					Name:   "lmstudio-b",
					Engine: "lmstudio",
					URL:    bURL,
					Models: []manifest.Model{{ID: "model-b"}},
				}},
			},
		},
	}
}

func TestRouter_RoutesByModel(t *testing.T) {
	a := newFakeUpstream(t, "a")
	b := newFakeUpstream(t, "b")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), b.URL().String()), fallback.URL())

	cases := []struct {
		modelID string
		want    *fakeUpstream
	}{
		{"model-a", a},
		{"model-b", b},
		{"unknown", fallback},
		{"", fallback},
	}
	for _, tc := range cases {
		t.Run(tc.modelID, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, tc.modelID)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if got := tc.want.calls; len(got) != 1 {
				t.Fatalf("expected exactly 1 call to %s, got %d", tc.want.name, len(got))
			}
			tc.want.calls = nil
			// Other upstreams must not have been hit.
			for _, other := range []*fakeUpstream{a, b, fallback} {
				if other == tc.want {
					continue
				}
				if len(other.calls) != 0 {
					t.Fatalf("upstream %s saw unexpected request: %+v", other.name, other.calls)
				}
			}
		})
	}
}

// A multimodal request (base64 image) easily exceeds the read cap; it must
// still route to the model's backend, not silently fall back.
func TestRouter_RoutesLargeMultimodalBody(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")
	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	bigImage := strings.Repeat("A", 2<<20) // ~2 MiB, well past MaxCompletionBodyBytes
	body := fmt.Sprintf(
		`{"model":"model-a","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`,
		bigImage,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(a.calls) != 1 {
		t.Fatalf("expected the large request to route to backend a, got a=%d fallback=%d",
			len(a.calls), len(fallback.calls))
	}
	if len(fallback.calls) != 0 {
		t.Fatalf("large request wrongly hit the fallback: %d call(s)", len(fallback.calls))
	}
	if a.calls[0].body != body {
		t.Errorf("forwarded body was altered/truncated (len got=%d want=%d)", len(a.calls[0].body), len(body))
	}
}

// sttManifest builds a manifest with one LLM service and one STT service on
// separate backends.
func sttManifest(llmURL, sttURL string) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig-a",
			Services: []manifest.Service{
				{Name: "vllm", Engine: "vllm", URL: llmURL, Models: []manifest.Model{{ID: "model-a"}}},
				{Name: "asr", Type: "stt", Engine: "vllm", URL: sttURL, Models: []manifest.Model{{ID: "whisper-1"}}},
			},
		}},
	}
}

// A transcription request must stream to the STT backend, not the LLM one or
// the fallback.
func TestRouter_RoutesTranscriptionToSTTBackend(t *testing.T) {
	llm := newFakeUpstream(t, "llm")
	stt := newFakeUpstream(t, "stt")
	fallback := newFakeUpstream(t, "fallback")
	r := New(sttManifest(llm.URL().String(), stt.URL().String()), fallback.URL())

	body := "--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n--b--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(stt.calls) != 1 {
		t.Fatalf("expected 1 call to STT backend, got %d", len(stt.calls))
	}
	if stt.calls[0].path != "/v1/audio/transcriptions" {
		t.Errorf("STT upstream path = %q, want /v1/audio/transcriptions", stt.calls[0].path)
	}
	for _, other := range []*fakeUpstream{llm, fallback} {
		if len(other.calls) != 0 {
			t.Fatalf("transcription wrongly hit %s: %+v", other.name, other.calls)
		}
	}
}

// With no STT service declared, transcription falls back to the env upstream
// rather than 404ing — mirrors the model-routing fallback.
func TestRouter_TranscriptionFallsBackWhenNoSTT(t *testing.T) {
	llm := newFakeUpstream(t, "llm")
	fallback := newFakeUpstream(t, "fallback")
	r := New(twoBackendManifest(llm.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(fallback.calls) != 1 {
		t.Fatalf("expected fallback to serve transcription, got %d call(s)", len(fallback.calls))
	}
}

// Image generations + edits must route to the image-typed backend.
func TestRouter_RoutesImagesToImageBackend(t *testing.T) {
	llm := newFakeUpstream(t, "llm")
	img := newFakeUpstream(t, "img")
	fallback := newFakeUpstream(t, "fallback")
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig-a",
			Services: []manifest.Service{
				{Name: "vllm", Engine: "vllm", URL: llm.URL().String(), Models: []manifest.Model{{ID: "model-a"}}},
				{Name: "imgsvc", Type: "image", Engine: "other", URL: img.URL().String(), Models: []manifest.Model{{ID: "sdxl"}}},
			},
		}},
	}
	r := New(m, fallback.URL())

	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		img.calls = nil
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"prompt":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		if len(img.calls) != 1 || img.calls[0].path != path {
			t.Fatalf("%s did not route to image backend: %+v", path, img.calls)
		}
	}
	if len(llm.calls) != 0 || len(fallback.calls) != 0 {
		t.Fatalf("image requests leaked to llm=%d fallback=%d", len(llm.calls), len(fallback.calls))
	}
}

// Video generations must route to the video-typed backend, hitting its single
// POST /generate endpoint (not an OpenAI-shaped path).
func TestRouter_RoutesVideosToVideoBackend(t *testing.T) {
	llm := newFakeUpstream(t, "llm")
	vid := newFakeUpstream(t, "vid")
	fallback := newFakeUpstream(t, "fallback")
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig-a",
			Services: []manifest.Service{
				{Name: "vllm", Engine: "vllm", URL: llm.URL().String(), Models: []manifest.Model{{ID: "model-a"}}},
				{Name: "ltx", Type: "video", Engine: "other", URL: vid.URL().String(), Models: []manifest.Model{{ID: "ltx-2"}}},
			},
		}},
	}
	r := New(m, fallback.URL())

	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"prompt":"a fox in snow"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if len(vid.calls) != 1 || !strings.HasSuffix(vid.calls[0].path, "/generate") {
		t.Fatalf("video request did not route to /generate on the video backend: %+v", vid.calls)
	}
	if vid.calls[0].body != `{"prompt":"a fox in snow"}` {
		t.Fatalf("video request body not forwarded verbatim: %q", vid.calls[0].body)
	}
	if len(llm.calls) != 0 || len(fallback.calls) != 0 {
		t.Fatalf("video request leaked to llm=%d fallback=%d", len(llm.calls), len(fallback.calls))
	}
}

// TTS synthesize + list_voices must route to the tts-typed backend.
func TestRouter_RoutesTtsToTtsBackend(t *testing.T) {
	llm := newFakeUpstream(t, "llm")
	tts := newFakeUpstream(t, "tts")
	fallback := newFakeUpstream(t, "fallback")
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig-a",
			Services: []manifest.Service{
				{Name: "vllm", Engine: "vllm", URL: llm.URL().String(), Models: []manifest.Model{{ID: "model-a"}}},
				{Name: "riva", Type: "tts", Engine: "other", URL: tts.URL().String(), Models: []manifest.Model{{ID: "magpie"}}},
			},
		}},
	}
	r := New(m, fallback.URL())

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/audio/synthesize"},
		{http.MethodGet, "/v1/audio/list_voices"},
	}
	for _, tc := range cases {
		tts.calls = nil
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.path, w.Code)
		}
		if len(tts.calls) != 1 || tts.calls[0].path != tc.path {
			t.Fatalf("%s did not route to tts backend: %+v", tc.path, tts.calls)
		}
	}
	if len(llm.calls) != 0 || len(fallback.calls) != 0 {
		t.Fatalf("tts requests leaked to llm=%d fallback=%d", len(llm.calls), len(fallback.calls))
	}
}

func TestNewWithProbe_SetsMaxModelLen(t *testing.T) {
	a := newFakeUpstream(t, "a")
	b := newFakeUpstream(t, "b")
	fallback := newFakeUpstream(t, "fallback")
	r := NewWithProbe(
		twoBackendManifest(a.URL().String(), b.URL().String()),
		fallback.URL(),
		map[string]int{"model-a": 10000},
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, w.Body.String())
	}
	byID := map[string]*int{}
	for i := range got.Data {
		byID[got.Data[i].ID] = got.Data[i].MaxModelLen
	}
	if byID["model-a"] == nil || *byID["model-a"] != 10000 {
		t.Errorf("model-a max_model_len = %v, want 10000", byID["model-a"])
	}
	if byID["model-b"] != nil {
		t.Errorf("model-b max_model_len = %v, want nil (not probed)", *byID["model-b"])
	}
}

func TestProbeContextLengths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"model-a","max_model_len":10000},{"id":"no-len"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	m := &manifest.Manifest{
		SchemaVersion: 1,
		Hosts: []manifest.Host{{ID: "h", Services: []manifest.Service{{
			Name: "vllm", Engine: "vllm", URL: srv.URL + "/v1",
			Models: []manifest.Model{{ID: "model-a"}},
		}}}},
	}
	got := ProbeContextLengths(m, 2*time.Second)
	if got["model-a"] != 10000 {
		t.Errorf("model-a = %d, want 10000", got["model-a"])
	}
	if _, ok := got["no-len"]; ok {
		t.Errorf("entry without max_model_len should be absent, got %d", got["no-len"])
	}
}

func TestProbeContextLengths_UnreachableIsSafe(t *testing.T) {
	m := &manifest.Manifest{
		Hosts: []manifest.Host{{ID: "h", Services: []manifest.Service{{
			Name: "x", URL: "http://127.0.0.1:1/v1", Models: []manifest.Model{{ID: "m"}},
		}}}},
	}
	got := ProbeContextLengths(m, 500*time.Millisecond)
	if len(got) != 0 {
		t.Errorf("expected empty map for an unreachable upstream, got %v", got)
	}
}

func TestRouter_RewritesPath_NoV1V1(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	body := `{"model":"model-a","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(a.calls) != 1 {
		t.Fatalf("upstream a not called: %+v", a.calls)
	}
	got := a.calls[0].path
	if got != "/v1/chat/completions" {
		t.Errorf("path forwarded = %q, want /v1/chat/completions (no /v1/v1 doubling)", got)
	}
}

func TestRouter_PreservesRequestBody(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	body := `{"model":"model-a","messages":[{"role":"user","content":"hello"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(a.calls) != 1 {
		t.Fatalf("expected one call, got %+v", a.calls)
	}
	if a.calls[0].body != body {
		t.Errorf("body forwarded = %q\nwant %q", a.calls[0].body, body)
	}
}

func TestRouter_AddsBackendHeader(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"model-a"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("X-Inference-Club-Backend"); got != "vllm-a" {
		t.Errorf("X-Inference-Club-Backend = %q, want vllm-a", got)
	}
}

func TestRouter_ModelsListedFromManifest(t *testing.T) {
	a := newFakeUpstream(t, "a")
	b := newFakeUpstream(t, "b")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), b.URL().String()), fallback.URL())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, w.Body.String())
	}
	ids := map[string]string{}
	for _, m := range got.Data {
		ids[m.ID] = m.OwnedBy
	}
	if ids["model-a"] != "vllm-a" {
		t.Errorf("model-a owned_by = %q, want vllm-a (from %+v)", ids["model-a"], got.Data)
	}
	if ids["model-b"] != "lmstudio-b" {
		t.Errorf("model-b owned_by = %q, want lmstudio-b (from %+v)", ids["model-b"], got.Data)
	}
	if len(a.calls) != 0 || len(b.calls) != 0 || len(fallback.calls) != 0 {
		t.Errorf("upstreams hit during /v1/models — should be assembled from manifest only: a=%d b=%d fb=%d",
			len(a.calls), len(b.calls), len(fallback.calls))
	}
}

func TestRouter_OtherV1PathsFallback(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"model-a","input":"hi"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(fallback.calls) != 1 {
		t.Errorf("expected fallback to receive /v1/embeddings, got fallback=%+v a=%+v",
			fallback.calls, a.calls)
	}
	if len(a.calls) != 0 {
		t.Errorf("manifest backend hit for non-completions path: %+v", a.calls)
	}
}

func TestRouter_NoManifest_AllToFallback(t *testing.T) {
	fallback := newFakeUpstream(t, "fallback")
	r := New(nil, fallback.URL())

	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"x"}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
	if len(fallback.calls) != 3 {
		t.Errorf("expected 3 fallback hits, got %d (%+v)", len(fallback.calls), fallback.calls)
	}
}

func TestRouter_DedupesBackendsBySharedURL(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	// Two services pointing at the SAME upstream URL should share one
	// reverse proxy — common when one vLLM instance serves several
	// models under different YAML service entries.
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig",
			Services: []manifest.Service{
				{
					Name:   "svc-1",
					Engine: "vllm",
					URL:    a.URL().String(),
					Models: []manifest.Model{{ID: "model-x"}},
				},
				{
					Name:   "svc-2",
					Engine: "vllm",
					URL:    a.URL().String(),
					Models: []manifest.Model{{ID: "model-y"}},
				},
			},
		}},
	}

	r := New(m, fallback.URL())
	if got := r.Backends(); len(got) != 1 {
		t.Errorf("Backends() = %v, want exactly 1 entry (deduped)", got)
	}
	if got := r.ModelOwner("model-x"); got != "svc-1" {
		t.Errorf("ModelOwner(model-x) = %q, want svc-1 (first service to declare URL)", got)
	}
	if got := r.ModelOwner("model-y"); got != "svc-1" {
		t.Errorf("ModelOwner(model-y) = %q, want svc-1 (shares backend)", got)
	}
}

func TestRouter_TruncatedBodyFallsBackSafely(t *testing.T) {
	a := newFakeUpstream(t, "a")
	fallback := newFakeUpstream(t, "fallback")

	r := New(twoBackendManifest(a.URL().String(), "http://unused.invalid/v1"), fallback.URL())

	// Body larger than MaxCompletionBodyBytes — router shouldn't try to
	// parse a 1MB+ blob just to peek at the model field, so it falls
	// back. The body still needs to reach the upstream intact.
	pad := bytes.Repeat([]byte("x"), MaxCompletionBodyBytes)
	body := fmt.Sprintf(`{"model":"model-a","filler":"%s"}`, pad)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(fallback.calls) != 1 {
		t.Fatalf("expected fallback to receive truncated request, got fb=%d a=%d",
			len(fallback.calls), len(a.calls))
	}
	if fallback.calls[0].body != body {
		t.Errorf("body length forwarded = %d, want %d (full body must pass through)",
			len(fallback.calls[0].body), len(body))
	}
}

// --- scrape (Firecrawl, PRD 12) ---------------------------------------------

// firecrawlFake stands in for Firecrawl: it records the request it got and
// replies with Firecrawl's {"success":true,"data":{markdown,metadata}} shape.
func firecrawlFake(t *testing.T, gotPath, gotBody *string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotPath = r.URL.Path
		*gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"data":{"markdown":"# Hello\n\nworld","metadata":{"title":"Hi","sourceURL":"https://ex.com/p"}}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestRouter_RoutesScrapeToFirecrawl(t *testing.T) {
	var gotPath, gotBody string
	fire := firecrawlFake(t, &gotPath, &gotBody)
	llm := newFakeUpstream(t, "llm")
	fallback := newFakeUpstream(t, "fallback")

	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{
			ID: "rig-a",
			Services: []manifest.Service{
				{Name: "vllm", Engine: "vllm", URL: llm.URL().String(), Models: []manifest.Model{{ID: "model-a"}}},
				{Name: "firecrawl", Type: "scrape", Engine: "other", URL: fire.URL + "/v1"},
			},
		}},
	}
	r := New(m, fallback.URL())

	req := httptest.NewRequest(http.MethodPost, "/v1/scrape", strings.NewReader(`{"url":"https://ex.com/p"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if gotPath != "/v1/scrape" {
		t.Fatalf("upstream path = %q, want /v1/scrape", gotPath)
	}
	if !strings.Contains(gotBody, `"formats"`) || !strings.Contains(gotBody, "markdown") {
		t.Fatalf("agent did not request a markdown format: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"url":"https://ex.com/p"`) {
		t.Fatalf("url not forwarded: %s", gotBody)
	}
	if got := w.Body.String(); got != "# Hello\n\nworld" {
		t.Fatalf("body = %q, want the extracted markdown (not Firecrawl's JSON envelope)", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type = %q, want text/markdown", ct)
	}
	if w.Header().Get("X-Scrape-Title") != "Hi" {
		t.Fatalf("X-Scrape-Title = %q", w.Header().Get("X-Scrape-Title"))
	}
	if w.Header().Get("X-Scrape-Source-Url") != "https://ex.com/p" {
		t.Fatalf("X-Scrape-Source-Url = %q", w.Header().Get("X-Scrape-Source-Url"))
	}
	if len(fallback.calls) != 0 {
		t.Fatalf("fallback should not be hit, got %d calls", len(fallback.calls))
	}
}

func TestRouter_ScrapeWithoutBackendReturns503(t *testing.T) {
	fallback := newFakeUpstream(t, "fallback")
	r := New(twoBackendManifest(fallback.URL().String(), fallback.URL().String()), fallback.URL())
	req := httptest.NewRequest(http.MethodPost, "/v1/scrape", strings.NewReader(`{"url":"https://ex.com"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestRouter_ScrapeRequiresURL(t *testing.T) {
	var gotPath, gotBody string
	fire := firecrawlFake(t, &gotPath, &gotBody)
	fallback := newFakeUpstream(t, "fallback")
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Agent:         manifest.Agent{Name: "club-host"},
		Hosts: []manifest.Host{{ID: "rig", Services: []manifest.Service{
			{Name: "firecrawl", Type: "scrape", Engine: "other", URL: fire.URL + "/v1"},
		}}},
	}
	r := New(m, fallback.URL())
	req := httptest.NewRequest(http.MethodPost, "/v1/scrape", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no url)", w.Code)
	}
	if gotPath != "" {
		t.Fatalf("upstream should not be called for a url-less request, hit %q", gotPath)
	}
}
