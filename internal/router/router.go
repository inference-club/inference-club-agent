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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
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
	// MaxModelLen is the served context window, probed from the upstream's
	// /v1/models (vLLM reports it). Omitted when unknown so the server can
	// fall back to other sources.
	MaxModelLen *int `json:"max_model_len,omitempty"`
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
	apiKey string // optional; sent as Authorization: Bearer on proxied requests
}

// Router is the http.Handler that fronts the agent's tailnet listener.
// It is immutable once built; SIGHUP reloads should construct a new
// Router and atomically swap it in (see main.go).
type Router struct {
	fallback     *backend
	backends     []*backend // ordered, deduped by URL
	byModel      map[string]*backend
	byType       map[string]*backend // service type ("stt"/"tts") → first backend
	manifestData modelsResponse      // pre-built /v1/models payload
}

// New builds a Router with no probe data — context lengths are unknown and
// omitted from /v1/models. Kept pure (no network) so startup and tests don't
// block on upstreams.
func New(m *manifest.Manifest, fallbackURL *url.URL) *Router {
	return NewWithProbe(m, fallbackURL, nil)
}

// NewWithProbe is New plus a map of served model id → max context length
// (from ProbeContextLengths), surfaced as max_model_len in /v1/models. A nil
// map or missing entry simply omits the field, leaving the server to fall
// back to the HuggingFace value or blank.
func NewWithProbe(m *manifest.Manifest, fallbackURL *url.URL, ctxLens map[string]int) *Router {
	r := &Router{
		fallback: newBackend("fallback", fallbackURL, ""),
		byModel:  map[string]*backend{},
		byType:   map[string]*backend{},
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
					b = newBackend(svc.Name, bURL, svc.APIKey)
					urlToBackend[key] = b
					r.backends = append(r.backends, b)
				}
				// First backend of each non-LLM type wins. STT (and later
				// TTS) endpoints route by type, not by model: the request is
				// multipart, so we don't peek a JSON model field.
				if st := svc.ServiceType(); st != "llm" {
					if _, seen := r.byType[st]; !seen {
						r.byType[st] = b
					}
					// A tts service that advertises voice-cloning (Dia) also
					// answers the dedicated /v1/voice/generations path. Index it
					// under a synthetic "voice" type so it's chosen over a plain
					// Riva tts service sharing the box.
					if st == "tts" && hasFeature(svc.Features, "voice-cloning") {
						if _, seen := r.byType["voice"]; !seen {
							r.byType["voice"] = b
						}
					}
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
					entry := modelEntry{ID: served, Object: "model", Created: now, OwnedBy: svc.Name}
					if n, ok := ctxLens[served]; ok && n > 0 {
						v := n
						entry.MaxModelLen = &v
					}
					r.manifestData.Data = append(r.manifestData.Data, entry)
				}
			}
		}
	}
	return r
}

// ProbeContextLengths asks each upstream's /v1/models for its served context
// window (vLLM reports it as max_model_len) and returns served-id → length.
// Best-effort: unreachable servers, non-200s, missing fields, and parse
// errors are skipped (and just absent from the map), so the caller always
// gets a usable result and never blocks beyond `timeout` per upstream.
func ProbeContextLengths(m *manifest.Manifest, timeout time.Duration) map[string]int {
	out := map[string]int{}
	if m == nil {
		return out
	}
	client := &http.Client{Timeout: timeout}
	seenURL := map[string]bool{}
	for _, h := range m.Hosts {
		for _, svc := range h.Services {
			if svc.URL == "" || seenURL[svc.URL] {
				continue
			}
			seenURL[svc.URL] = true
			endpoint := strings.TrimSuffix(svc.URL, "/") + "/models"
			req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
			if svc.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+svc.APIKey)
			}
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("probe %s: %v (context length unknown)", endpoint, err)
				continue
			}
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					log.Printf("probe %s: HTTP %d (context length unknown)", endpoint, resp.StatusCode)
					return
				}
				var body struct {
					Data []struct {
						ID          string `json:"id"`
						MaxModelLen *int   `json:"max_model_len"`
					} `json:"data"`
				}
				if jerr := json.NewDecoder(resp.Body).Decode(&body); jerr != nil {
					log.Printf("probe %s: decode: %v", endpoint, jerr)
					return
				}
				for _, d := range body.Data {
					if d.MaxModelLen != nil && *d.MaxModelLen > 0 {
						out[d.ID] = *d.MaxModelLen
					}
				}
			}()
		}
	}
	return out
}

// newBackend builds a reverse proxy for one upstream URL with the same
// path-rewrite + flushing semantics the agent has used since 005355e.
func newBackend(name string, target *url.URL, apiKey string) *backend {
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
		// When the operator configured an api_key for this backend, the agent
		// authenticates to it with that key — overriding any inbound
		// Authorization (which carries the consumer's inference.club credential,
		// not the backend's). When unset, the inbound header is left untouched.
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy %s error: %v", name, err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
	}
	return &backend{target: target, proxy: proxy, name: name, apiKey: apiKey}
}

// ServeHTTP dispatches to the right upstream:
//   - GET /v1/models → assembled from the manifest (no upstream call).
//   - POST /v1/chat/completions or /v1/completions → backend that owns
//     the requested model, or the fallback if the model isn't declared.
//   - POST /v1/audio/transcriptions → the STT-typed backend (multipart is
//     streamed through untouched), or the fallback if none is declared.
//   - POST /v1/images/generations|edits → the image-typed backend (JSON or
//     multipart streamed through), or the fallback if none is declared.
//   - /v1/audio/synthesize | /v1/audio/list_voices → the tts-typed backend
//     (the NVIDIA Riva speech paths the backend adapts /v1/audio/speech to).
//   - POST /v1/3d/generations → the mesh-typed backend, remapped to TRELLIS.2's
//     POST /generate (multipart streamed through), or the fallback if none.
//   - everything else under /v1/ → the fallback.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/v1/models":
		r.serveModels(w, req)
	case req.Method == http.MethodPost && (req.URL.Path == "/v1/chat/completions" || req.URL.Path == "/v1/completions"):
		r.serveCompletions(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/audio/transcriptions":
		r.serveByType(w, req, "stt")
	case req.Method == http.MethodPost &&
		(req.URL.Path == "/v1/images/generations" || req.URL.Path == "/v1/images/edits"):
		r.serveByType(w, req, "image")
	case req.URL.Path == "/v1/audio/synthesize" || req.URL.Path == "/v1/audio/list_voices":
		// Text-to-speech: the inference.club backend adapts the OpenAI
		// /v1/audio/speech request to the NVIDIA Riva synthesize / list_voices
		// paths, which we forward to the tts service.
		r.serveByType(w, req, "tts")
	case req.Method == http.MethodPost && req.URL.Path == "/v1/3d/generations":
		// Image-to-3D: the multipart image+options request goes to the mesh
		// service, remapped to TRELLIS.2's single POST /generate endpoint.
		r.serveMesh(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/music/generations":
		// Text-to-music: ACE-Step is async (submit a job, poll for it, then
		// download the rendered audio). The agent runs that whole loop and
		// replies with the finished audio bytes, so inference.club can treat
		// music like every other one-shot modality.
		r.serveMusic(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/videos/generations":
		// Text/image-to-video (LTX-2): a one-shot JSON request the agent
		// forwards to the video server's single POST /generate endpoint,
		// streaming the rendered MP4 bytes straight back.
		r.serveVideo(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/voice/generations":
		// Voice cloning / text-to-dialogue (Dia): the inference.club backend
		// builds a multipart request (script + optional audio prompt +
		// transcript + sampling fields) which we forward to Dia's single POST
		// /generate, streaming the audio/wav (and x-seed / x-sample-rate /
		// x-duration-seconds headers) straight back.
		r.serveVoice(w, req)
	default:
		r.fallback.proxy.ServeHTTP(w, req)
	}
}

// serveByType forwards a request to the first backend of the given service
// type, streaming the body through untouched (important: transcription bodies
// are multipart audio uploads that can be large — we never buffer them).
// Falls back to the env upstream when no service of that type is declared.
func (r *Router) serveByType(w http.ResponseWriter, req *http.Request, serviceType string) {
	target := r.fallback
	if b, ok := r.byType[serviceType]; ok {
		target = b
	}
	w.Header().Set("X-Inference-Club-Backend", target.name)
	target.proxy.ServeHTTP(w, req)
}

// serveMesh forwards an image-to-3D request to the mesh backend. Unlike the
// OpenAI-shaped paths, TRELLIS.2 exposes a single POST /generate (no /v1), so
// we rewrite the inbound /v1/3d/generations to /v1/generate before proxying:
// the backend director trims the /v1 prefix and joins the backend's base path,
// yielding /generate on a base-URL backend (http://host:8000). The multipart
// body (image + options) streams through untouched, and the GLB bytes +
// X-Trellis-Metadata response header are passed straight back.
func (r *Router) serveMesh(w http.ResponseWriter, req *http.Request) {
	target := r.fallback
	if b, ok := r.byType["mesh"]; ok {
		target = b
	}
	req.URL.Path = "/v1/generate"
	w.Header().Set("X-Inference-Club-Backend", target.name)
	target.proxy.ServeHTTP(w, req)
}

// serveVoice forwards a voice-cloning / dialogue request to the Dia backend (a
// tts service advertising the voice-cloning feature, indexed under the
// synthetic "voice" type). Like mesh, Dia exposes a single POST /generate (no
// /v1), so we rewrite the inbound /v1/voice/generations to /v1/generate; the
// director then trims /v1 and joins the backend base path, yielding /generate.
// The multipart body (script + optional audio prompt + transcript + sampling
// fields) streams through untouched, and the audio/wav bytes plus the
// x-seed / x-sample-rate / x-duration-seconds headers pass straight back.
func (r *Router) serveVoice(w http.ResponseWriter, req *http.Request) {
	b, ok := r.byType["voice"]
	if !ok {
		http.Error(w, "no voice-cloning service configured", http.StatusServiceUnavailable)
		return
	}
	req.URL.Path = "/v1/generate"
	w.Header().Set("X-Inference-Club-Backend", b.name)
	b.proxy.ServeHTTP(w, req)
}

// hasFeature reports whether a declared-features list contains feat.
func hasFeature(features []string, feat string) bool {
	for _, f := range features {
		if f == feat {
			return true
		}
	}
	return false
}

// MaxVideoBodyBytes is the initial read buffer for an inbound video request.
// Larger than the chat cap because an image-to-video body carries the optional
// first-frame image inline as a base64 string; readCappedBody still drains
// anything past it, so this only avoids a second read for typical images.
const MaxVideoBodyBytes = 16 << 20 // 16 MiB

// videoMaxWait bounds a single text/image-to-video generation. LTX renders can
// run for minutes; the inbound request context still cancels earlier if the
// caller (inference.club) gives up first.
const videoMaxWait = 10 * time.Minute

// videoClient performs the upstream /generate call. No per-call Timeout — the
// request context (videoMaxWait) is the single source of truth for the deadline.
var videoClient = &http.Client{}

// serveVideo forwards a text/image-to-video request to the video backend. The
// LTX-2 server exposes a single POST /generate (no /v1 prefix) that accepts the
// JSON body verbatim and returns raw video/mp4 bytes plus an X-LTX-Params header
// describing the resolved (snapped) parameters. The agent buffers the inbound
// JSON, forwards it, and streams the MP4 straight back — inference.club owns the
// request schema; the agent owns only the transport.
func (r *Router) serveVideo(w http.ResponseWriter, req *http.Request) {
	b, ok := r.byType["video"]
	if !ok {
		http.Error(w, "no video service configured", http.StatusServiceUnavailable)
		return
	}
	body, _, err := readCappedBody(req.Body, MaxVideoBodyBytes)
	_ = req.Body.Close()
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("X-Inference-Club-Backend", b.name)

	base := strings.TrimSuffix(b.target.String(), "/")
	ctx, cancel := context.WithTimeout(req.Context(), videoMaxWait)
	defer cancel()

	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/generate", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	resp, err := videoClient.Do(upReq)
	if err != nil {
		log.Printf("video %s: generation failed: %v", b.name, err)
		http.Error(w, "video generation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		log.Printf("video %s: upstream HTTP %d: %s", b.name, resp.StatusCode, strings.TrimSpace(string(raw)))
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "video/mp4"
	}
	w.Header().Set("Content-Type", ct)
	// Pass through the resolved (snapped) generation params so the backend can
	// record the true width/height/fps/frame-count it rendered at.
	if p := resp.Header.Get("X-LTX-Params"); p != "" {
		w.Header().Set("X-LTX-Params", p)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("video %s: stream error: %v", b.name, err)
	}
}

// Music generation (ACE-Step) is a poll-based async API: POST /release_task
// returns a task_id, POST /query_result reports progress (status 0 running /
// 1 succeeded / 2 failed) and, on success, the path to the rendered audio,
// which we then GET from /v1/audio. serveMusic hides all of that behind one
// synchronous request. The inbound JSON body is forwarded verbatim to
// /release_task — inference.club owns ACE-Step's request schema; the agent
// owns only the submit/poll/download protocol.
const (
	musicPollInterval = 1500 * time.Millisecond
	musicMaxWait      = 8 * time.Minute
)

// musicClient bounds each individual upstream call. The overall job wait is
// bounded separately by musicMaxWait via the request context.
var musicClient = &http.Client{Timeout: 90 * time.Second}

func (r *Router) serveMusic(w http.ResponseWriter, req *http.Request) {
	b, ok := r.byType["music"]
	if !ok {
		http.Error(w, "no music service configured", http.StatusServiceUnavailable)
		return
	}
	body, _, err := readCappedBody(req.Body, MaxCompletionBodyBytes)
	_ = req.Body.Close()
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("X-Inference-Club-Backend", b.name)

	base := strings.TrimSuffix(b.target.String(), "/")
	ctx, cancel := context.WithTimeout(req.Context(), musicMaxWait)
	defer cancel()

	taskID, err := musicSubmit(ctx, b, base, body)
	if err != nil {
		log.Printf("music %s: submit failed: %v", b.name, err)
		http.Error(w, "music submit failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	file, err := musicPoll(ctx, b, base, taskID)
	if err != nil {
		log.Printf("music %s: generation failed (task %s): %v", b.name, taskID, err)
		http.Error(w, "music generation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	audio, ct, err := musicDownload(ctx, b, base, file)
	if err != nil {
		log.Printf("music %s: download failed (task %s): %v", b.name, taskID, err)
		http.Error(w, "music download failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if ct == "" {
		ct = "audio/mpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
	_, _ = w.Write(audio)
}

// musicAuth applies the backend's optional api_key to an upstream call.
func musicAuth(req *http.Request, b *backend) {
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
}

// musicSubmit posts the generation job and returns its task_id.
func musicSubmit(ctx context.Context, b *backend, base string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/release_task", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	musicAuth(req, b)
	resp, err := musicClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("submit HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env struct {
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode submit response: %w", err)
	}
	if env.Data.TaskID == "" {
		return "", fmt.Errorf("submit returned no task_id: %s", strings.TrimSpace(string(raw)))
	}
	return env.Data.TaskID, nil
}

// musicPoll queries /query_result until the job finishes, returning the path
// to the rendered audio. Errors out on failure or when the context expires.
func musicPoll(ctx context.Context, b *backend, base, taskID string) (string, error) {
	payload, _ := json.Marshal(map[string][]string{"task_id_list": {taskID}})
	ticker := time.NewTicker(musicPollInterval)
	defer ticker.Stop()
	for {
		file, done, err := musicQueryOnce(ctx, b, base, payload)
		if err != nil {
			return "", err
		}
		if done {
			return file, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out after %s", musicMaxWait)
		case <-ticker.C:
		}
	}
}

// musicQueryOnce does a single poll. done=true means the job succeeded and
// `file` holds the audio path; an error means the job failed.
func musicQueryOnce(ctx context.Context, b *backend, base string, payload []byte) (file string, done bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/query_result", bytes.NewReader(payload))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	musicAuth(req, b)
	resp, err := musicClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("query HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env struct {
		Data []struct {
			Status int    `json:"status"`
			Result string `json:"result"`
			// progress_text carries the upstream's human-readable status — and,
			// crucially, the error when a job reports success but couldn't save
			// the audio (e.g. a missing codec). Surface it so failures are
			// diagnosable instead of a bare "no audio file".
			ProgressText string `json:"progress_text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", false, fmt.Errorf("decode query response: %w", err)
	}
	if len(env.Data) == 0 {
		return "", false, nil // not visible yet — keep polling
	}
	rec := env.Data[0]
	pt := strings.TrimSpace(rec.ProgressText)
	switch rec.Status {
	case 1: // succeeded
		f, ferr := musicFileFromResult(rec.Result)
		if ferr != nil {
			// Job "succeeded" but produced no usable file — the real reason is
			// usually in progress_text (e.g. a save/codec error upstream).
			if pt != "" {
				return "", false, fmt.Errorf("%v — upstream: %s", ferr, pt)
			}
			return "", false, ferr
		}
		return f, true, nil
	case 2: // failed
		msg := musicErrorFromResult(rec.Result)
		if pt != "" {
			return "", false, fmt.Errorf("%s (%s)", msg, pt)
		}
		return "", false, fmt.Errorf("%s", msg)
	default: // 0 = queued/running
		return "", false, nil
	}
}

// musicFileFromResult pulls the audio path out of the (JSON-string) result
// field, which holds an array of generated items (or, defensively, one object).
func musicFileFromResult(result string) (string, error) {
	result = strings.TrimSpace(result)
	if result == "" {
		return "", fmt.Errorf("succeeded but result was empty")
	}
	var items []struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal([]byte(result), &items); err == nil {
		for _, it := range items {
			if strings.TrimSpace(it.File) != "" {
				return it.File, nil
			}
		}
	}
	var one struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal([]byte(result), &one); err == nil && strings.TrimSpace(one.File) != "" {
		return one.File, nil
	}
	return "", fmt.Errorf("no audio file in result")
}

// musicErrorFromResult extracts a human-readable error from a failed job's
// result, falling back to the raw string.
func musicErrorFromResult(result string) string {
	var items []struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &items); err == nil {
		for _, it := range items {
			if strings.TrimSpace(it.Error) != "" {
				return it.Error
			}
		}
	}
	if s := strings.TrimSpace(result); s != "" {
		return s
	}
	return "generation failed"
}

// musicDownload fetches the rendered audio. `file` is normally a server path
// like "/v1/audio?path=..."; an absolute URL is honored as-is.
func musicDownload(ctx context.Context, b *backend, base, file string) ([]byte, string, error) {
	dl := file
	if !strings.HasPrefix(file, "http://") && !strings.HasPrefix(file, "https://") {
		dl = base + "/" + strings.TrimPrefix(file, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dl, nil)
	if err != nil {
		return nil, "", err
	}
	musicAuth(req, b)
	resp, err := musicClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, "", fmt.Errorf("download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return audio, resp.Header.Get("Content-Type"), nil
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
