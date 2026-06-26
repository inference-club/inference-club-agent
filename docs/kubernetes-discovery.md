# Kubernetes discovery — labelling your Services for inference.club

This is the complete, copy-paste reference for how the agent finds the inference
Services in your cluster and what each label and annotation does. To *install*
the agent, see the [chart README](../charts/inference-club-agent/README.md); for
the big picture, the [top-level README](../README.md). AI assistants should start
at [`AGENTS.md`](../AGENTS.md).

---

## Publish your first model in 5 minutes

Assumes the agent is already installed (see the
[chart README](../charts/inference-club-agent/README.md)) and watching the
`inference-club` namespace, and that you have an OpenAI-compatible inference
server running as a Service in that namespace — say `svc/my-vllm` on port 8000
that serves `/v1` and `GET /v1/models`.

```bash
# 1. Mark the Service as managed — this is the one required step.
kubectl -n inference-club label svc my-vllm inference-club.com/managed=true

# 2. Tell the agent where the OpenAI API lives under the Service (usually /v1).
kubectl -n inference-club annotate svc my-vllm inference-club.com/base-path=/v1

# 3. (Optional) say what kind of service it is — defaults to llm.
kubectl -n inference-club label svc my-vllm \
  inference-club.com/type=llm inference-club.com/engine=vllm

# 4. Watch the agent pick it up (within one poll interval, default 30s).
kubectl -n inference-club logs -l app.kubernetes.io/name=inference-club-agent -f
#   → discovered manifest from kubernetes (1 services)
```

That's it — open https://inference.club/dashboard and your provider is **online**
with the model auto-discovered from `GET /v1/models`. To override the auto-listed
model with a proper name, modalities, and context window, add a `models`
annotation ([Model cards](#model-cards)). To stop publishing it later, without
touching the running Service:

```bash
kubectl -n inference-club label svc my-vllm inference-club.com/managed-
```

The rest of this guide explains every field and gives full examples for each
service type. Read on when you want more than a plain auto-discovered LLM.

---

## The one idea

> **A Kubernetes Service labelled `inference-club.com/managed=true` is published
> to inference.club. Labels select; annotations describe.**

You do **not** hand-write a list of hosts, GPUs, or URLs. The agent derives all
of that from the cluster. You only describe *intent* on each Service: "publish
this, it's a TTS service, here are its models."

```
┌─ you write ─────────────────────────┐     ┌─ agent derives from the cluster ──┐
│ which Services to publish (label)    │     │ which node a service runs on      │
│ the service type (llm/stt/tts/…)     │     │ the node's GPU model + VRAM       │
│ the engine (display only)            │ ──▶ │ the in-cluster URL (svc DNS)      │
│ model cards (optional annotation)    │     │ the exact image + command running │
│ a per-service upstream key (optional)│     │ pod phase / ready / restarts      │
└─────────────────────────────────────┘     └───────────────────────────────────┘
```

| You declare (on the Service) | Agent derives (from the cluster) |
|---|---|
| `managed` label = publish it | `url` → `http://<svc>.<ns>.svc.cluster.local:<port>` + base-path |
| `type`, `engine` labels | node address + hostname (from the backing pod's node) |
| `models`, `features`, `base-path`, `api-key-secret` annotations | GPU vendor / model / VRAM / count (from node labels) |
| (nothing about hardware) | the running `image` + `command` + `args` |

---

## The schema

### Labels (selection + the few things worth filtering on)

| label | required | values | meaning |
|---|---|---|---|
| `inference-club.com/managed` | **yes** | `"true"` | discovery selector — the only required field |
| `inference-club.com/type` | no | `llm` (default), `stt`, `tts`, `image`, `mesh`, `music`, `video`, `audio-enhance`, `scrape` | service kind; controls which `/v1/*` route reaches it |
| `inference-club.com/engine` | no | `vllm`, `lmstudio`, `ollama`, `sglang`, `llamacpp`, `tgi`, `other` (default `other`) | display only, no behaviour |

### Annotations (the structured payload that doesn't fit in label syntax)

| annotation | meaning |
|---|---|
| `inference-club.com/base-path` | appended to the derived Service URL — almost always `/v1` |
| `inference-club.com/models` | YAML list of model cards (see [Model cards](#model-cards)). Optional for engines that serve `GET /v1/models`. |
| `inference-club.com/features` | comma list of service-level features, e.g. `timestamps`, `voice-cloning,dialogue` |
| `inference-club.com/api-key-secret` | name of a Secret (same namespace) whose `api-key` value the agent sends upstream as `Authorization: Bearer …`. **Stripped from the manifest — never uploaded to inference.club.** |
| `inference-club.com/port` | port *name or number* — only needed when the Service exposes more than one port (otherwise the first port is used) |

> **Why `base-path`?** The agent builds the upstream URL from the Service's DNS
> name + port. The base-path is the suffix where the OpenAI-compatible API
> lives. For most servers that's `/v1`. A handful of services (async music/video
> backends) expose their API at the root and take **no** base-path — match what
> your upstream actually serves.

---

## Minimal: publish a Service in two commands

If your Service already serves `/v1` and exposes `GET /v1/models` (vLLM,
Ollama, SGLang, …), you don't even need a models annotation:

```bash
kubectl -n inference-club label svc my-vllm \
  inference-club.com/managed=true \
  inference-club.com/type=llm \
  inference-club.com/engine=vllm

kubectl -n inference-club annotate svc my-vllm \
  inference-club.com/base-path=/v1
```

Within one poll interval (default 30s) the provider goes **online** and the
models auto-populate. The rest of this doc is about declaring richer metadata
and the non-LLM service types.

---

## Model cards

inference.club shows each model's modalities, features, and context window in
its catalog and playground. **You declare these — they are never guessed.** Put
a YAML list in the `inference-club.com/models` annotation. `id` is the only
required field; `hf` is strongly recommended (it pools the same model across
nodes and links its HuggingFace page).

```yaml
inference-club.com/models: |
  - id: qwen3-30b-a3b          # served id, used for routing — REQUIRED
    hf: Qwen/Qwen3-30B-A3B     # HuggingFace repo — strongly recommended
    name: Qwen3 30B A3B        # human-friendly display name
    input_modalities: [text]   # defaults from `type` when omitted
    output_modalities: [text]  # defaults from `type` when omitted
    features: [reasoning, tools]
    context_length: 32768      # declared ceiling; live-probed window wins when known
    quantization: fp8          # per-deployment
```

Modalities default from the service `type` (`llm`→text/text, `stt`→audio/text,
`tts`→text/audio, `image`→[text,image]/image), so a plain text LLM can omit
them. A malformed `models` annotation degrades gracefully to "no declared
models" rather than dropping the Service — `doctor` flags it.

---

## Full examples by service type

These are clean, generic versions of real deployments. Each shows the Service
object only — your Deployment/StatefulSet is unchanged; the agent finds the pod
behind the Service's selector. Put everything in the namespace the agent
watches (default `inference-club`).

### LLM (vLLM) — auto-discovers models

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-vllm
  namespace: inference-club
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: llm
    inference-club.com/engine: vllm
  annotations:
    inference-club.com/base-path: /v1
spec:
  selector: { app: my-vllm }
  ports:
    - name: http
      port: 8000
```

### LLM (LM Studio) — declared model cards + an upstream API key

```yaml
apiVersion: v1
kind: Service
metadata:
  name: lmstudio-headless
  namespace: inference-club
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: llm
    inference-club.com/engine: lmstudio
  annotations:
    inference-club.com/base-path: /v1
    inference-club.com/api-key-secret: lmstudio-key
    inference-club.com/models: |
      - id: qwen/qwen3-27b
        name: Qwen3 27B
        input_modalities: [text, image]
        output_modalities: [text]
        features: [reasoning, tools]
        context_length: 8192
        quantization: Q4_K_M
spec:
  selector: { app: lmstudio-headless }
  ports:
    - name: http
      port: 1234
      targetPort: http
```

The referenced Secret (its `api-key` value is sent upstream, never uploaded):

```bash
kubectl -n inference-club create secret generic lmstudio-key \
  --from-literal=api-key=sk-your-lmstudio-key
```

### STT (speech-to-text) — with word/segment timestamps

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-asr
  namespace: inference-club
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: stt          # routes /v1/audio/transcriptions here
    inference-club.com/engine: other
  annotations:
    inference-club.com/base-path: /v1
    inference-club.com/features: timestamps   # ONLY if it returns real timings (verbose_json)
    inference-club.com/models: |
      - id: my-asr-model
        hf: org/my-asr-model
        input_modalities: [audio]
        output_modalities: [text]
spec:
  selector: { app: my-asr }
  ports:
    - name: http
      port: 8105
      targetPort: http
```

### TTS (text-to-speech)

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-tts
  namespace: inference-club
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: tts          # backend adapts /v1/audio/speech to this
    inference-club.com/engine: other
  annotations:
    inference-club.com/base-path: /v1
    inference-club.com/models: |
      - id: my-tts-voice
        input_modalities: [text]
        output_modalities: [audio]
spec:
  selector: { app: my-tts }
  ports:
    - name: http
      port: 9000
```

### Other generative types

The same pattern covers the rest — only the `type` label and the model `id`
change. Routing per type:

| `type` | OpenAI-compatible route the backend sends here |
|---|---|
| `image` | `/v1/images/generations`, `/v1/images/edits` |
| `music` | `/v1/music/generations` |
| `video` | `/v1/videos/generations` |
| `mesh` | 3D mesh generation |
| `audio-enhance` | `/v1/audio/enhance` |
| `scrape` | `/v1/scrape` (URL → markdown) |

```yaml
# image example
metadata:
  name: my-image
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: image
    inference-club.com/engine: other
  annotations:
    inference-club.com/base-path: /v1
    inference-club.com/models: |
      - id: my-image-model
        input_modalities: [text, image]
        output_modalities: [image]
```

> Some async backends (e.g. music/video servers) expose their API at the root,
> not under `/v1`. In that case **omit `base-path`** and the agent uses the
> Service URL as-is. Always match what your upstream actually serves.

---

## External (out-of-cluster) endpoints

Running a server outside the cluster — e.g. LM Studio on a laptop on the same
LAN — and want to publish it through the same label system? Create a
**selector-less Service** carrying the usual labels plus a hand-written
`EndpointSlice` pointing at the external address:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: desktop-lmstudio
  namespace: inference-club
  labels:
    inference-club.com/managed: "true"
    inference-club.com/type: llm
    inference-club.com/engine: lmstudio
  annotations:
    inference-club.com/base-path: /v1
spec:
  ports:
    - name: http
      port: 1234
  # no selector
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: desktop-lmstudio-1
  namespace: inference-club
  labels:
    kubernetes.io/service-name: desktop-lmstudio
addressType: IPv4
ports:
  - name: http
    port: 1234
endpoints:
  - addresses: ["192.168.1.50"]   # the external box's LAN IP
```

The agent treats the endpoint address as the "host" (GPU facts unknown, since
there's no node behind it) and routes through cluster DNS exactly as for
in-cluster Services.

---

## Node labels (GPU facts + host identity)

The agent reads two things from **Nodes**, no labelling required for basic use:

- **GPU count** from `nvidia.com/gpu` allocatable (the NVIDIA device plugin).
- **GPU model + VRAM** from `nvidia.com/gpu.product` and `nvidia.com/gpu.memory`
  labels — these only exist if you run **GPU Feature Discovery** (GFD) + Node
  Feature Discovery. Without GFD, count still works; model/VRAM stay empty.

Optional, for nicer host names on your profile:

- `inference-club.com/box=<name>` on a node makes that string the manifest host
  id (otherwise the Kubernetes node name is used). Set it at k3s install time,
  e.g. `--node-label inference-club.com/box=rig-01`.

> Some accelerators (e.g. NVIDIA GB10 / unified memory) report VRAM that NVML
> can't read. If GFD gets it wrong, override it with a Node Feature Discovery
> `NodeFeatureRule` that hard-sets `nvidia.com/gpu.memory`.

---

## Optional: live GPU stats

Static GPU facts (count/model/total VRAM) come from node labels above. For
**live** VRAM/utilisation on your dashboard, the agent scrapes two per-node
endpoints if they exist — both optional:

| component | hostPort | env override | what it gives |
|---|---|---|---|
| [`dcgm-exporter`](https://github.com/NVIDIA/dcgm-exporter) | `9400` | `DCGM_SCRAPE_PORT` (`0`=off) | per-GPU VRAM + utilisation |
| `vram-reporter` (this repo's DaemonSet) | `9401` | `VRAM_REPORTER_PORT` (`0`=off) | per-process VRAM attributed to pods → managed services |

`vram-reporter` is built from [`Dockerfile.vram-reporter`](../Dockerfile.vram-reporter)
in this repo; it runs as a `hostPID` DaemonSet on your GPU nodes. dcgm-exporter
needs `runtimeClassName: nvidia`. Deploy them on the nodes that run inference
(typically via a `nodeAffinity` on `inference-club.com/box`). When neither is
present the scrape is simply skipped.

---

## How the agent reads the cluster (and the RBAC it needs)

One read-only poll loop (interval `discovery.interval`, default 30s; SIGHUP /
`helm upgrade` forces an immediate re-list):

1. `LIST services` filtered by `labelSelector=inference-club.com/managed=true`
2. `LIST pods` — to find the pod backing each Service (node + image + command)
3. `LIST endpointslices` — for selector-less external endpoints
4. `LIST nodes` (+ `metrics.k8s.io`) — GPU facts and live usage
5. `GET secrets/<name>` only for Services that set `api-key-secret`

The Helm chart grants exactly this and nothing more (see
[`templates/rbac.yaml`](../charts/inference-club-agent/templates/rbac.yaml)):
namespaced `get,list` on services/pods/endpointslices, namespaced `get` on
secrets, cluster-scoped `get,list` on nodes and node/pod metrics. The agent
never creates, updates, or deletes anything in your cluster.

**A Service is only in the manifest while it is actually serving.** A labelled
Service with a selector but no Running pod is dropped until a pod lands — the
manifest reports what *is* serving, so a crash-looping model won't masquerade as
available.

---

## Conventions worth copying (from a working cluster)

Not required, but they keep a multi-GPU home cluster sane:

- **One directory per service**, `services/<name>/<name>.yaml`, holding the
  Deployment + Service together. Directory name == Service name == pod `app:`
  label.
- Pin each model to a node with `nodeSelector: { inference-club.com/box: <box> }`.
- `strategy.type: Recreate` for single-GPU exclusivity (don't run two pods
  fighting over one GPU).
- Claim a GPU exclusively with `resources.limits.nvidia.com/gpu: 1`, or share
  one across pods with `runtimeClassName: nvidia` + `NVIDIA_VISIBLE_DEVICES=all`
  and **no** resource claim.
- Keep a short comment above each Service noting how discovery routes it.

---

## Quick checklist

- [ ] Agent installed and watching the right namespace (chart README).
- [ ] Each inference Service has `inference-club.com/managed=true`.
- [ ] `type` set if it's not a plain LLM.
- [ ] `base-path` set (usually `/v1`) — or omitted for root-API backends.
- [ ] Model cards declared (or the engine serves `GET /v1/models`).
- [ ] Upstream-key Secret created and referenced via `api-key-secret`, if needed.
- [ ] (Optional) GFD installed for GPU model/VRAM; `inference-club.com/box` for host names.
- [ ] Provider shows **online** within ~30s at https://inference.club/dashboard.
</content>
