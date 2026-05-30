// Package router builds a model-aware reverse proxy from a service
// manifest. The agent receives all OpenAI traffic on its tailnet listener
// and forwards each /v1/chat/completions or /v1/completions request to
// the upstream service whose manifest entry advertises the requested
// model id. Aggregating /v1/models is done from the manifest itself so
// the listing matches what the operator declared in YAML.
//
// Anything outside /v1/chat/completions, /v1/completions, and /v1/models
// (including unknown models) falls back to the agent's pre-manifest
// upstream — env LOCAL_LLM_URL — so existing single-LLM operators see
// no behavior change.
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
)

// MaxCompletionBodyBytes is the initial read-buffer size for an inbound
// chat-completion body. It's only a starting allocation: readCappedBody
// drains anything past it too, and we always peek the model from the full
// body — so large multimodal requests route correctly, they just allocate a
// bit more. Sized to fit text-only turns without a second read.
const MaxCompletionBodyBytes = 1 << 20 // 1 MiB

// modelEntry is one item in the OpenAI-format /v1/models response.
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

// backend is one upstream OpenAI-compatible server. Multiple services in
// the manifest can share a backend if they point at the same URL — we
// dedupe by URL string so a four-model vLLM instance only spins up one
// reverse proxy.
type backend struct {
	target *url.URL
	proxy  *httputil.ReverseProxy
	name   string // first service.name we saw for this URL — used in logs
}

// Router is the http.Handler that fronts the agent's tailnet listener.
// It is immutable once built; SIGHUP reloads should construct a new
// Router and atomically swap it in (see main.go).
type Router struct {
	fallback     *backend
	backends     []*backend         // ordered, deduped by URL
	byModel      map[string]*backend
	manifestData modelsResponse     // pre-built /v1/models payload
}

// New builds a Router from a manifest and a fallback upstream URL. The
// fallback handles unknown models, the synthesized-from-env manifest
// case, and any /v1/* path other than chat/completions/models.
func New(m *manifest.Manifest, fallbackURL *url.URL) *Router {
	r := &Router{
		fallback: newBackend("fallback", fallbackURL),
		byModel:  map[string]*backend{},
	}

	// Aggregate models response from manifest declarations. Dedupe model
	// ids — first service wins, mirroring how byModel routes them.
	seenModels := map[string]struct{}{}
	now := time.Now().Unix()
	r.manifestData.Object = "list"

	if m != nil {
		urlToBackend := map[string]*backend{}
		for _, h := range m.Hosts {
			for _, svc := range h.Services {
				bURL, err := url.Parse(svc.URL)
				if err != nil || bURL.Host == "" {
					log.Printf("router: skipping service %q with unparseable URL %q: %v", svc.Name, svc.URL, err)
					continue
				}
				key := bURL.String()
				b, ok := urlToBackend[key]
				if !ok {
					b = newBackend(svc.Name, bURL)
					urlToBackend[key] = b
					r.backends = append(r.backends, b)
				}
				for _, m := range svc.Models {
					served := m.ServedID()
					if served == "" {
						continue
					}
					if _, dup := seenModels[served]; dup {
						continue
					}
					seenModels[served] = struct{}{}
					r.byModel[served] = b
					r.manifestData.Data = append(r.manifestData.Data, modelEntry{
						ID: served, Object: "model", Created: now, OwnedBy: svc.Name,
					})
				}
			}
		}
	}
	return r
}

// newBackend builds a reverse proxy for one upstream URL with the same
// path-rewrite + flushing semantics the agent has used since 005355e.
func newBackend(name string, target *url.URL) *backend {
	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Capture the inbound path before defaultDirector clobbers it via
		// singleJoiningSlash(target.Path, req.URL.Path). Without this, an
		// upstream URL ending in /v1 produces /v1/v1/models on the wire.
		origPath := req.URL.Path
		defaultDirector(req)
		req.URL.Path = strings.TrimSuffix(target.Path, "/") + strings.TrimPrefix(origPath, "/v1")
		req.Host = target.Host
		req.Header.Set("X-Forwarded-Host", target.Host)
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy %s error: %v", name, err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
	}
	return &backend{target: target, proxy: proxy, name: name}
}

// ServeHTTP dispatches to the right upstream:
//   - GET /v1/models → assembled from the manifest (no upstream call).
//   - POST /v1/chat/completions or /v1/completions → backend that owns
//     the requested model, or the fallback if the model isn't declared.
//   - everything else under /v1/ → the fallback.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/v1/models":
		r.serveModels(w, req)
	case req.Method == http.MethodPost && (req.URL.Path == "/v1/chat/completions" || req.URL.Path == "/v1/completions"):
		r.serveCompletions(w, req)
	default:
		r.fallback.proxy.ServeHTTP(w, req)
	}
}

func (r *Router) serveModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if len(r.manifestData.Data) == 0 {
		// No manifest models declared — return an empty list. The agent
		// is intentionally not probing each backend for live /v1/models;
		// inference.club's central proxy already does that aggregation
		// from the DB. This endpoint is a sanity check, not the source
		// of truth for clients.
		_ = json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: []modelEntry{}})
		return
	}
	_ = json.NewEncoder(w).Encode(r.manifestData)
}

func (r *Router) serveCompletions(w http.ResponseWriter, req *http.Request) {
	// Read at most MaxCompletionBodyBytes so we can peek at the model
	// field, then put the bytes back so the proxied request still sees
	// the original body.
	body, _, err := readCappedBody(req.Body, MaxCompletionBodyBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("X-Inference-Club-Body-Buffered", "1")

	// Peek the model regardless of body size. readCappedBody always returns
	// the FULL body (it drains everything past the cap), so multimodal
	// requests — base64 image/audio/video easily exceed 1 MiB — must still
	// route to the model's backend, not silently fall back.
	target := r.fallback
	var probe struct {
		Model string `json:"model"`
	}
	if jerr := json.Unmarshal(body, &probe); jerr == nil && probe.Model != "" {
		if b, ok := r.byModel[probe.Model]; ok {
			target = b
		}
	}
	w.Header().Set("X-Inference-Club-Backend", target.name)
	target.proxy.ServeHTTP(w, req)
}

// readCappedBody reads up to limit+1 bytes; if more data is available it
// returns truncated=true and the original limit-sized prefix. The caller
// is responsible for restoring the body on the request.
func readCappedBody(r io.Reader, limit int) ([]byte, bool, error) {
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(r, buf)
	switch err {
	case nil:
		// Read limit+1 bytes — there's more behind it. Drain the rest
		// into the buffer so the proxied request body is intact.
		rest, derr := io.ReadAll(r)
		if derr != nil {
			return nil, false, derr
		}
		return append(buf[:n], rest...), true, nil
	case io.ErrUnexpectedEOF, io.EOF:
		return buf[:n], false, nil
	default:
		return nil, false, err
	}
}

// Backends returns the deduped list of upstream backends in declaration
// order. Exposed for the doctor subcommand and tests.
func (r *Router) Backends() []string {
	out := make([]string, 0, len(r.backends))
	for _, b := range r.backends {
		out = append(out, b.target.String())
	}
	return out
}

// ModelOwner returns the backend name that will serve a given model id,
// or "" if the model isn't declared in the manifest. Useful for logs and
// the doctor subcommand.
func (r *Router) ModelOwner(modelID string) string {
	if b, ok := r.byModel[modelID]; ok {
		return b.name
	}
	return ""
}
