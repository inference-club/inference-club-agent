// Package manifest loads and validates the operator's service manifest —
// a YAML description of the hosts in their home network, each host's GPU,
// and the LLM services running on each host.
//
// The manifest is uploaded to inference.club (see upload.go) so the
// operator's public profile at inference.club/<github_login> can render
// the same picture the operator wrote in YAML.
//
// See `docs/plans/service-manifest.md` in the inference.club repo for the
// authoritative shape and field semantics.
package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only schema_version this build understands. The
// server keeps a small accept-list and may accept older versions, but the
// agent always emits the newest it knows.
const SchemaVersion = 1

// Limits — see `docs/plans/service-manifest.md` §6. Mirrors the
// server-side validator in `apps/inference/manifest_validator.py`.
const (
	MaxRawYAMLBytes = 64 * 1024
	MaxHosts        = 50
	MaxServices     = 100
	MaxStringLen    = 1024
)

var (
	gpuVendors = map[string]struct{}{
		"nvidia": {}, "amd": {}, "apple": {}, "intel": {},
	}
	engines = map[string]struct{}{
		"vllm": {}, "lmstudio": {}, "ollama": {}, "sglang": {},
		"llamacpp": {}, "tgi": {}, "other": {},
	}
	// serviceTypes is the *what* a service provides, orthogonal to engine.
	// Defaults to "llm" when omitted, so pre-existing manifests stay valid.
	// "mesh" is image-to-3D (e.g. TRELLIS.2): one image in, a textured GLB out.
	// "music" is text-to-music (e.g. ACE-Step): a prompt in, a rendered song out.
	// "video" is text/image-to-video (e.g. LTX-2): a prompt (+ optional
	// first-frame image) in, a rendered MP4 out.
	// "scrape" is URL→markdown (e.g. Firecrawl): a URL in, extracted markdown
	// out. A "high-level" service — it calls an LLM under the hood for clean
	// extraction, but to the agent it's just another typed backend.
	serviceTypes = map[string]struct{}{
		"llm": {}, "stt": {}, "tts": {}, "image": {}, "mesh": {}, "music": {}, "video": {},
		"scrape": {},
	}
)

// Manifest is the root document.
type Manifest struct {
	SchemaVersion int `yaml:"schema_version" json:"schema_version"`
	// Discovery records where this manifest came from: "" / "static" for
	// agent.yaml, "kubernetes" when built from the cluster. The server uses
	// it to decide whether live cluster state (/cluster/state) is available
	// for this provider. Additive — older manifests simply omit it.
	Discovery string `yaml:"discovery,omitempty" json:"discovery,omitempty"`
	Agent     Agent  `yaml:"agent" json:"agent"`
	Hosts     []Host `yaml:"hosts" json:"hosts"`
}

// Agent identifies this agent to inference.club. “Name“ is also the
// lookup key the server uses to bind a manifest to a Provider row.
type Agent struct {
	Name       string `yaml:"name" json:"name"`
	Hostname   string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	ListenPort int    `yaml:"listen_port,omitempty" json:"listen_port,omitempty"`
}

// Host is one machine in the operator's home network.
type Host struct {
	ID       string    `yaml:"id" json:"id"`
	Hostname string    `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Address  string    `yaml:"address,omitempty" json:"address,omitempty"`
	GPU      *GPU      `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	Notes    string    `yaml:"notes,omitempty" json:"notes,omitempty"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
}

// GPU is enough for display — vendor + model + VRAM. We deliberately
// don't try to enumerate every spec; this is operator-entered text.
type GPU struct {
	Vendor string  `yaml:"vendor,omitempty" json:"vendor,omitempty"`
	Model  string  `yaml:"model,omitempty" json:"model,omitempty"`
	VRAMGB float64 `yaml:"vram_gb,omitempty" json:"vram_gb,omitempty"`
	Count  int     `yaml:"count,omitempty" json:"count,omitempty"`
}

// Service is one OpenAI-compatible inference endpoint running on a Host.
// “Name“ doubles as the router-key for the multi-backend router (see the
// agent ROADMAP).
type Service struct {
	Name string `yaml:"name" json:"name"`
	// Type is what the service provides: "llm" (default), "stt", "tts",
	// "image", or "mesh" (image-to-3D). Drives which /v1 endpoint the router
	// forwards here.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Features are operator-declared capabilities of THIS deployment, e.g.
	// ["timestamps"] for an STT service launched with a ForcedAligner so
	// verbose_json returns word timings. Per-deployment, because the same
	// model may or may not expose a feature depending on how it was served.
	Features []string `yaml:"features,omitempty" json:"features,omitempty"`
	Engine   string   `yaml:"engine" json:"engine"`
	URL      string   `yaml:"url" json:"url"`
	Models   []Model  `yaml:"models,omitempty" json:"models,omitempty"`
	Command  string   `yaml:"command,omitempty" json:"command,omitempty"`
	// APIKey, when set, is sent as `Authorization: Bearer <key>` on every
	// request the router proxies to this service's URL — e.g. an LM Studio or
	// vLLM server started with an API key. It is a LOCAL-ONLY secret: it is
	// excluded from JSON (`json:"-"`) and stripped from the manifest before
	// upload (see Redacted), so it NEVER reaches inference.club.
	APIKey string            `yaml:"api_key,omitempty" json:"-"`
	Extra  map[string]string `yaml:"extra,omitempty" json:"extra,omitempty"`
}

// ServiceType returns the declared type, defaulting to "llm" when omitted.
func (s Service) ServiceType() string {
	if t := strings.TrimSpace(s.Type); t != "" {
		return t
	}
	return "llm"
}

// Model is one model a service serves.
//
//   - ID is the *served* id — the exact string the backend (vLLM et al.)
//     answers to, used for routing.
//   - Hf is the HuggingFace repo id (e.g. "Qwen/Qwen3-30B-A3B"). It gives the
//     model its canonical identity on inference.club, which pools the same
//     model across providers. When ID is omitted the served id defaults to the
//     HF id (vLLM serves under the HF id unless --served-model-name is set).
//
// The remaining fields are operator-declared CAPABILITIES, surfaced on
// inference.club's catalog and the playground. All optional — the operator
// knows exactly what they serve, so these are declared, never guessed. When
// the modality lists are omitted they default from the service Type
// (llm→text/text, stt→audio/text, tts→text/audio, image→[text,image]/image).
type Model struct {
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	Hf string `yaml:"hf,omitempty" json:"hf,omitempty"`
	// Name is a human-friendly display name (e.g. "Qwen3 30B A3B").
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// InputModalities / OutputModalities, e.g. ["text", "image"] / ["text"].
	InputModalities  []string `yaml:"input_modalities,omitempty" json:"input_modalities,omitempty"`
	OutputModalities []string `yaml:"output_modalities,omitempty" json:"output_modalities,omitempty"`
	// Features are model-identity capabilities, e.g. ["reasoning", "tools"].
	Features []string `yaml:"features,omitempty" json:"features,omitempty"`
	// ContextLength is the declared context-window ceiling. The live-probed
	// served window (max_model_len) takes precedence when known.
	ContextLength int `yaml:"context_length,omitempty" json:"context_length,omitempty"`
	// Quantization, e.g. "fp8" / "int4" (per-deployment).
	Quantization string `yaml:"quantization,omitempty" json:"quantization,omitempty"`
}

// ServedID is the id the backend answers to: the explicit ID, or the HF id
// when ID is omitted. Mirrors the server's _model_served_id.
func (m Model) ServedID() string {
	if strings.TrimSpace(m.ID) != "" {
		return strings.TrimSpace(m.ID)
	}
	return strings.TrimSpace(m.Hf)
}

// LoadResult bundles the parsed manifest with the raw YAML bytes — the
// server wants both, so we don't have to re-encode to push.
type LoadResult struct {
	Manifest *Manifest
	Raw      []byte
}

// Load reads, parses, and validates a manifest from disk. Returns the
// parsed manifest, the raw bytes, and any validation errors. A non-nil
// LoadResult means the file parsed; “errs“ may still be non-empty if
// validation failed.
func Load(path string) (*LoadResult, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(raw) > MaxRawYAMLBytes {
		return nil, nil, fmt.Errorf(
			"manifest %s is %d bytes, exceeds %d byte limit",
			path, len(raw), MaxRawYAMLBytes,
		)
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("parse manifest: %w", err)
	}
	errs := Validate(&m)
	return &LoadResult{Manifest: &m, Raw: raw}, errs, nil
}

// Redacted returns a copy of the manifest with every per-service api_key
// cleared, so the secret never leaves the agent. Only the Services slices are
// deep-copied (that's all we mutate); everything else is shared by reference,
// which is safe because callers only read the result (to marshal it).
func (m *Manifest) Redacted() *Manifest {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Hosts = make([]Host, len(m.Hosts))
	for i, h := range m.Hosts {
		if len(h.Services) > 0 {
			svcs := make([]Service, len(h.Services))
			copy(svcs, h.Services)
			for j := range svcs {
				svcs[j].APIKey = ""
			}
			h.Services = svcs
		}
		cp.Hosts[i] = h
	}
	return &cp
}

// RedactedForUpload returns the manifest's raw YAML and parsed JSON map with
// all per-service api_keys stripped — the exact bytes/struct safe to send to
// inference.club. We re-marshal the redacted manifest rather than forwarding
// the operator's literal file so a secret can never ride along in raw_yaml.
// Tradeoff: the server-stored YAML is normalized (comments/formatting dropped);
// the operator's local agent.yaml is untouched.
func (m *Manifest) RedactedForUpload() (rawYAML []byte, parsed map[string]any, err error) {
	red := m.Redacted()
	rawYAML, err = yaml.Marshal(red)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal redacted manifest: %w", err)
	}
	parsed, err = red.AsJSONMap()
	if err != nil {
		return nil, nil, err
	}
	return rawYAML, parsed, nil
}

// AsJSONMap converts the manifest to a generic map[string]any so we can
// PUT it to inference.club as JSON — the server validates JSON, not YAML.
func (m *Manifest) AsJSONMap() (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate inspects the whole structure and returns every problem it
// finds, so the operator gets one round-trip with everything wrong instead
// of fix-one-find-next.
//
// Mirrors the server-side validator in
// “apps/inference/manifest_validator.py“. Keep them in sync.
func Validate(m *Manifest) []string {
	if m == nil {
		return []string{"manifest is nil"}
	}
	var errs []string

	if m.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Sprintf(
			"schema_version must be %d, got %d", SchemaVersion, m.SchemaVersion,
		))
	}

	if d := m.Discovery; d != "" && d != "static" && d != "kubernetes" {
		errs = append(errs, fmt.Sprintf(
			`discovery: must be omitted, "static" or "kubernetes", got %q`, d,
		))
	}

	if strings.TrimSpace(m.Agent.Name) == "" {
		errs = append(errs, "agent.name: required non-empty string")
	} else if len(m.Agent.Name) > MaxStringLen {
		errs = append(errs, fmt.Sprintf("agent.name: exceeds %d chars", MaxStringLen))
	}

	if len(m.Hosts) > MaxHosts {
		errs = append(errs, fmt.Sprintf(
			"hosts: exceeds %d entries (got %d)", MaxHosts, len(m.Hosts),
		))
	}

	seenHostIDs := map[string]struct{}{}
	seenServiceNames := map[string]struct{}{}
	totalServices := 0

	for hi, h := range m.Hosts {
		hp := fmt.Sprintf("hosts[%d]", hi)

		switch {
		case strings.TrimSpace(h.ID) == "":
			errs = append(errs, hp+".id: required non-empty string")
		case len(h.ID) > MaxStringLen:
			errs = append(errs, fmt.Sprintf("%s.id: exceeds %d chars", hp, MaxStringLen))
		default:
			if _, dup := seenHostIDs[h.ID]; dup {
				errs = append(errs, fmt.Sprintf("%s.id: duplicate host id %q", hp, h.ID))
			}
			seenHostIDs[h.ID] = struct{}{}
		}

		if h.GPU != nil {
			if h.GPU.Vendor != "" {
				if _, ok := gpuVendors[h.GPU.Vendor]; !ok {
					errs = append(errs, fmt.Sprintf(
						"%s.gpu.vendor: must be one of [amd apple intel nvidia], got %q",
						hp, h.GPU.Vendor,
					))
				}
			}
			if h.GPU.VRAMGB < 0 {
				errs = append(errs, hp+".gpu.vram_gb: must be non-negative")
			}
			if h.GPU.Count < 0 {
				errs = append(errs, hp+".gpu.count: must be non-negative")
			}
		}

		for _, fld := range []struct {
			name, val string
		}{
			{"hostname", h.Hostname}, {"address", h.Address}, {"notes", h.Notes},
		} {
			if len(fld.val) > MaxStringLen {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: exceeds %d chars", hp, fld.name, MaxStringLen,
				))
			}
		}

		for si, s := range h.Services {
			totalServices++
			sp := fmt.Sprintf("%s.services[%d]", hp, si)

			switch {
			case strings.TrimSpace(s.Name) == "":
				errs = append(errs, sp+".name: required non-empty string")
			case len(s.Name) > MaxStringLen:
				errs = append(errs, fmt.Sprintf("%s.name: exceeds %d chars", sp, MaxStringLen))
			default:
				if _, dup := seenServiceNames[s.Name]; dup {
					errs = append(errs, fmt.Sprintf(
						"%s.name: duplicate service name %q (must be unique across the whole manifest)",
						sp, s.Name,
					))
				}
				seenServiceNames[s.Name] = struct{}{}
			}

			if _, ok := engines[s.Engine]; !ok {
				errs = append(errs, fmt.Sprintf(
					"%s.engine: must be one of [llamacpp lmstudio ollama other sglang tgi vllm], got %q",
					sp, s.Engine,
				))
			}

			// type is optional and defaults to "llm"; only an explicit
			// out-of-set value is an error.
			if s.Type != "" {
				if _, ok := serviceTypes[s.Type]; !ok {
					errs = append(errs, fmt.Sprintf(
						"%s.type: must be one of [image llm mesh music stt tts video], got %q", sp, s.Type,
					))
				}
			}

			// features is an optional, free-form capability list (e.g.
			// "timestamps"). YAML already enforces []string; just bound each.
			for fi, f := range s.Features {
				if len(f) > MaxStringLen {
					errs = append(errs, fmt.Sprintf(
						"%s.features[%d]: exceeds %d chars", sp, fi, MaxStringLen,
					))
				}
			}

			if strings.TrimSpace(s.URL) == "" {
				errs = append(errs, sp+".url: required non-empty string")
			} else if u, err := url.Parse(s.URL); err != nil || u.Scheme == "" || u.Host == "" {
				errs = append(errs, fmt.Sprintf(
					"%s.url: must be an absolute URL with scheme and host (got %q)",
					sp, s.URL,
				))
			}

			if len(s.Command) > MaxStringLen {
				errs = append(errs, fmt.Sprintf(
					"%s.command: exceeds %d chars", sp, MaxStringLen,
				))
			}

			if len(s.APIKey) > MaxStringLen {
				errs = append(errs, fmt.Sprintf(
					"%s.api_key: exceeds %d chars", sp, MaxStringLen,
				))
			}

			// Per-model declared capabilities (all optional). YAML already
			// enforces the types; just bound strings and reject negatives.
			for mi, mdl := range s.Models {
				mp := fmt.Sprintf("%s.models[%d]", sp, mi)
				for _, fld := range []struct {
					name, val string
				}{{"name", mdl.Name}, {"quantization", mdl.Quantization}} {
					if len(fld.val) > MaxStringLen {
						errs = append(errs, fmt.Sprintf(
							"%s.%s: exceeds %d chars", mp, fld.name, MaxStringLen,
						))
					}
				}
				for _, grp := range []struct {
					name string
					vals []string
				}{
					{"input_modalities", mdl.InputModalities},
					{"output_modalities", mdl.OutputModalities},
					{"features", mdl.Features},
				} {
					for vi, v := range grp.vals {
						if len(v) > MaxStringLen {
							errs = append(errs, fmt.Sprintf(
								"%s.%s[%d]: exceeds %d chars", mp, grp.name, vi, MaxStringLen,
							))
						}
					}
				}
				if mdl.ContextLength < 0 {
					errs = append(errs, mp+".context_length: must be non-negative")
				}
			}
		}
	}

	if totalServices > MaxServices {
		errs = append(errs, fmt.Sprintf(
			"services: exceeds %d entries across all hosts (got %d)",
			MaxServices, totalServices,
		))
	}

	return errs
}

// SynthesizeFromEnv builds a one-host / one-service manifest from the
// pre-YAML env vars. This is the back-compat path: existing single-LLM
// users keep working with zero config changes.
func SynthesizeFromEnv(agentName, listenPort, localLLMURL string) *Manifest {
	port := 0
	fmt.Sscanf(listenPort, "%d", &port)
	if port == 0 {
		port = 443
	}
	if agentName == "" {
		agentName = "club-host"
	}
	if localLLMURL == "" {
		localLLMURL = "http://host.docker.internal:1234/v1"
	}
	return &Manifest{
		SchemaVersion: SchemaVersion,
		Agent: Agent{
			Name:       agentName,
			ListenPort: port,
		},
		Hosts: []Host{
			{
				ID: "default",
				Services: []Service{
					{
						Name:   agentName,
						Engine: "other",
						URL:    localLLMURL,
					},
				},
			},
		},
	}
}
