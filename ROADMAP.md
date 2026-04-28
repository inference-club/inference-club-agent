# Roadmap

A short, opinionated plan for the next phase of `inference-club-agent`.
Today the agent is a single Go binary that fronts **one** OpenAI-compatible
LLM server. The goal of this phase: make a single agent host **many** LLM
backends across **many** machines under one tailnet identity, with config
that scales beyond a wall of env vars.

**Scope is deliberately LLM-only.** TTS, image gen, embeddings, and other
modalities are out — getting LLM inference rock-solid is the priority.
See "What this roadmap is not going to do" at the bottom.

---

## The core architectural question — one agent, or many?

**Recommendation: one agent per home network (per "site"), fronting many
LLM backends via a YAML config.** Not one agent per GPU.

| | One per GPU box | **One per site (recommended)** |
|---|---|---|
| Tailscale nodes | N (eats device cap) | **1** |
| inference.club Providers | N (cluttered dashboard) | **1** |
| Setup work for a new GPU | install Docker on it, run agent | **edit one YAML line** |
| Failure isolation | per-box | per-backend (still good) |
| Latency cost | none | one extra LAN hop (~1 ms) |
| Adding a second LLM server (vLLM next to your Ollama) | another agent | **another YAML entry** |

The single-site model wins because the user's mental unit is "my house",
not "this specific 4090". Backends come and go (you swap a GPU, you try
vLLM vs Ollama, you add a Mac Studio, you upgrade to a new model server)
— the agent that owns the tailnet identity should outlive any one of them.

The agent presents itself to inference.club as a single OpenAI-compatible
endpoint. inference.club doesn't need to know there are 4 boxes behind it
— it sees one Provider with N models. All the multi-backend routing
happens inside the agent.

---

## Proposed config shape

A single `agent.yaml` (mounted at `/etc/inference-club-agent/agent.yaml`)
replaces most env vars. Env vars still cover **secrets** (`INFERENCE_CLUB_API_KEY`)
and **infra knobs** (`AGENT_STATE_DIR`, `INFERENCE_CLUB_URL`).

```yaml
# /etc/inference-club-agent/agent.yaml
agent:
  name: brian-home              # label in the inference.club dashboard
  hostname: club-host-brian     # tailnet hostname (server may rewrite)
  listen_port: 443

backends:
  - name: rtx-4090-vllm
    kind: openai                # speaks OpenAI-compat /v1/* natively
    url: http://192.168.1.10:8000/v1
    # Optional. If omitted, agent auto-discovers via GET /v1/models.
    models:
      - id: qwen3-30b-a3b
      - id: mistral-small-3.1

  - name: mac-studio-lmstudio
    kind: openai
    url: http://192.168.1.20:1234/v1
    # No models listed → agent will auto-discover and refresh every 5 min.

  - name: rtx-3090-ollama
    kind: openai
    url: http://192.168.1.30:11434/v1
    health:
      path: /api/tags          # Ollama's native health surface
      interval: 30s
```

Routing rules the agent enforces:

- `POST /v1/chat/completions` → backend whose `models[].id == body.model`.
- `POST /v1/completions` → same lookup.
- `GET /v1/models` → aggregate across **all** healthy backends, returning
  each model with `owned_by: <backend.name>` so inference.club can show
  the source rig in its dashboard.

If the model isn't found anywhere → `404` with a clear message ("no
configured backend serves model X"). If two backends both serve the
same model id → see Open Questions §3.

---

## Phased roadmap

Four tight phases. Each is one focused PR-sized chunk; ship in order.

### Phase 1 — YAML config + multi-LLM-backend

**The capability that changes the agent from "one box" to "one site".**

- Add a YAML config loader (`gopkg.in/yaml.v3` is sufficient; resist
  pulling in viper for this scope). Read `agent.yaml` from `--config /path`
  flag, env `AGENT_CONFIG_FILE`, then default
  `/etc/inference-club-agent/agent.yaml`.
- Backwards-compat: if no YAML present, synthesize a single backend from
  `LOCAL_LLM_URL` so existing single-LLM users keep working without
  changes.
- New `internal/router/` package: `body.model` → backend lookup with
  O(1) map.
- New `internal/backend/openai.go`: HTTP client + reverse proxy per
  backend. Each backend gets its own healthcheck goroutine.
- `/v1/models` aggregator that queries each healthy backend and de-dupes.
- Per-backend `last_seen` and `last_error` tracked in-memory and exposed
  at `/status` (see Phase 2).
- Fail open: if one backend is unhealthy, requests for its models return
  `503` with the backend's last error message; unrelated backends keep
  serving.

**Deliverable:** one agent, one tailnet node, multiple LLM URLs serving
multiple model names. Verified end-to-end via inference.club.

### Phase 2 — Operability

**Make it debuggable when something breaks at 11pm on a Sunday.**

- Structured JSON logs (`log/slog` from the stdlib is enough) with
  `backend=`, `model=`, `request_id=`, `latency_ms=`, `tokens_in=`,
  `tokens_out=` (where parseable from the upstream response).
- New `GET /status` (tailnet-only): per-backend state, model counts,
  last health-probe outcome, last proxy outcome. Renders both JSON and
  a tiny HTML page so the operator can `curl` it or open it in a browser.
- Optional `GET /metrics` Prometheus endpoint, gated behind
  `--metrics-listen :9090` so it's off by default.
- Hot-reload YAML on `SIGHUP` (and on file change via `fsnotify`).
  In-flight requests drain with a 5s timeout (see Open Questions §2).
- `inference-club-agent doctor` subcommand: dumps redacted config, pings
  each backend's `/v1/models`, prints actionable diagnostics for the
  common failure modes the README's troubleshooting section already
  enumerates.
- Better error pass-through: when an upstream backend returns an OpenAI
  error JSON, propagate it verbatim (don't re-wrap). Add an
  `X-Inference-Club-Backend: <name>` response header so clients can see
  which rig handled the request.

**Deliverable:** when something breaks, the operator can `docker logs`
and `curl /status` and immediately know which backend is sick and why.

### Phase 3 — Setup UX

**First-time install should not require reading the README.**

- `inference-club-agent init` interactive wizard:
  - Prompts for inference.club API key.
  - Probes the LAN (mDNS + a small port-scan of common LLM-server
    ports: 1234 / LM Studio, 8000 / vLLM, 11434 / Ollama, 8080 /
    llama.cpp) and *suggests* backends to add.
  - Writes `agent.yaml` and prints the `docker run` snippet to use.
- (Optional, lower priority) tiny browser UI bound to localhost-only
  for non-CLI users to add/remove backends and toggle models.
- Pre-flight check on boot: warn loudly (without crashing) if the
  configured `url`s are unreachable from inside the container — the
  most common Linux gotcha is forgetting `--add-host`.

**Deliverable:** a person who has an LLM running on their LAN can go
from "I have an inference.club account" to "my GPUs are sharing models
with the club" in under five minutes, without reading the README.

### Phase 4 — Release engineering

**Make the README's `docker run ghcr.io/...` line actually work.**

- GitHub Actions:
  - Build multi-arch image (linux/amd64, linux/arm64) on push to `main`.
  - Publish to `ghcr.io/inference-club/inference-club-agent`.
  - Tag releases on `v*` tags; tag `:latest` on `main`; tag
    `:v1.2.3` and `:v1` on releases.
- Smoke-test job: run the agent against a stub OpenAI server, verify
  `/v1/models` aggregation and `/v1/chat/completions` proxy work
  (including streaming).
- `CHANGELOG.md` with hand-curated release notes.
- Watchtower-friendly labels so users who set up auto-updates Just Get
  Them.
- Update README to reference the published image (drop the "build
  locally" caveat).

**Deliverable:** `docker run ghcr.io/inference-club/inference-club-agent:latest`
works as documented, with `docker pull` + `docker restart` as the upgrade
path.

---

## Coordination points with inference.club

For the LLM-only multi-backend scope, **no server-side changes are
required**. The agent presents itself as a single OpenAI surface with
N models; inference.club already supports a Provider with many
ProviderModels. Each model's `owned_by` field carries the originating
backend name for display purposes if the dashboard wants to show it.

The current `/api/inference/agent/register/` contract does not change.
Capabilities are still discovered by the central server probing
`/v1/models`.

(Optional, defer until felt-pain) `Provider.tier` or `Provider.region`
— once people have several agents (home + a friend's house + an
office), users may want to tag them. Skip until someone asks.

---

## Open questions for you

1. **Authentication between agent and backends.** Today we assume
   trusted LAN. Some setups want auth (LM Studio supports an API key,
   vLLM supports `--api-key`). Add a per-backend `api_key` field in
   YAML in Phase 1 or punt? My lean: punt to a follow-up — most home
   networks don't need it, and we can add the field without a breaking
   change later.
2. **Hot-reload semantics** when a model is removed mid-stream — drain
   in-flight requests, or kill them? My lean: drain with a 5s timeout
   (called out in Phase 2).
3. **Model name collision across backends.** Two rigs both running
   `qwen3-30b-a3b`. Round-robin? First-match? Pin via YAML? My lean
   for v1: first-match wins (deterministic, predictable), with a
   future `routing.strategy: round-robin | least-busy` knob in YAML
   when we have telemetry to drive "least-busy".
4. **Multiple inference.club identities per agent.** Edge case (e.g.
   you're in two clubs), but the YAML structure makes it easy to
   support — `agent.identities: [...]`. Probably YAGNI; flag if anyone
   asks.

---

## What this roadmap is **not** going to do

So we don't drift:

- **Anything other than LLM inference.** No TTS, no image generation,
  no video, no embeddings, no audio transcription. Each of those is
  worth doing eventually, but only after the LLM path is solid and we
  have real users on it. Adding a `modality` field to the YAML is a
  later call.
- **Run inference itself.** The agent is a router/proxy. It does not
  load models, manage GPU memory, or talk to CUDA.
- **Persist anything beyond local cache.** The Tailscale state and the
  cached auth key are the only on-disk state. Inference history,
  billing, user accounts — all server side.
- **Be reachable on the public internet.** The whole point is the
  tailnet. There is no plan to add public listeners or TLS termination.
- **Autoscale or schedule across hosts.** A fancy "smart router" with
  GPU utilization awareness and queue depth tracking is interesting,
  but Phases 1–3 first; revisit only after real users complain about
  unfairness.
- **Adapt non-OpenAI APIs.** Today every backend is `kind: openai`.
  We can add an adapter interface later if a popular LLM server
  doesn't speak OpenAI-compat (none come to mind), but YAGNI for now.
