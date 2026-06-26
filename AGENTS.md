# AGENTS.md — guide for AI assistants setting up inference-club-agent

You are probably reading this because someone said something like *"set up the
inference-club-agent"* or *"help me register my home LLMs on inference.club"*.
This file is a self-contained runbook. Humans: it doubles as a fast overview — the
full prose lives in [`README.md`](./README.md), the
[chart README](./charts/inference-club-agent/README.md), and the
[Kubernetes discovery guide](./docs/kubernetes-discovery.md).

## What this is

`inference-club-agent` lets a person register the OpenAI-compatible inference
Services running in their **Kubernetes / k3s cluster** with
[inference.club](https://inference.club), making them available over the internet
to anyone the user grants access to (configurable in the inference.club
dashboard). The agent runs *in* the cluster, dials *out* to inference.club, joins
a private Tailscale network, and reverse-proxies `/v1/*` requests back to the
user's labelled Services over cluster DNS. **No port forwarding, no public URL, no
Tailscale account needed** — inference.club mints an ephemeral tailnet key during
registration.

> **Kubernetes only.** The old Docker Compose / `agent.yaml` single-server setup
> has been removed. If a user asks for that, steer them to the Kubernetes path
> below. This repo is *only* the agent; the central service (Django + Nuxt) lives
> at https://github.com/briancaffey/inference.club.

## What you need from the user

- **A Kubernetes cluster** they have `kubectl` + `helm` access to (k3s is the
  reference target — it ships `metrics-server` and the `ServiceLB` load balancer).
- **At least one OpenAI-compatible inference server already running as a Service**
  in the cluster (vLLM, Ollama, SGLang, llama.cpp, LM Studio via an external
  endpoint, …). The agent does **not** start inference servers — it publishes the
  ones the user already runs.
- **An inference.club API key** from https://inference.club/dashboard. You cannot
  generate this — ask the user to paste it or set it as a Secret themselves.
  Treat it as a credential: never echo it back, commit it, or put it in a values
  file.

---

## Step 1 — install the agent (Helm)

```bash
kubectl create namespace inference-club

# Store the API key as a Secret (preferred — keeps it out of values files).
kubectl -n inference-club create secret generic inference-club-api-key \
  --from-literal=api-key=ic_live_xxxx

helm install club-agent ./charts/inference-club-agent \
  --namespace inference-club \
  --set agentName=<short-name-for-this-provider> \
  --set apiKey.existingSecret=inference-club-api-key
```

The chart sets `AGENT_DISCOVERY=kubernetes`, grants read-only RBAC, and starts
the poll loop. By default it watches the **release namespace** — so label
Services in `inference-club`, or set `discovery.namespace` to wherever the user's
inference Services live. Full values reference:
[chart README](./charts/inference-club-agent/README.md).

For production, also set `--set persistence.enabled=true` so the tailnet identity
survives pod restarts.

---

## Step 2 — label the Services to publish

The agent's source of truth is **Kubernetes Services labelled
`inference-club.com/managed=true`**. Labels select; annotations describe. This is
the schema — it's the heart of the setup:

**Labels** (selectors / enums):

| label | required | values | meaning |
|---|---|---|---|
| `inference-club.com/managed` | **yes** | `"true"` | publish this Service |
| `inference-club.com/type` | no | `llm` (default) `stt` `tts` `image` `mesh` `music` `video` `audio-enhance` `scrape` | what kind of service — controls `/v1/*` routing |
| `inference-club.com/engine` | no | `vllm` `lmstudio` `ollama` `sglang` `llamacpp` `tgi` `other` (default `other`) | display only |

**Annotations** (descriptive):

| annotation | meaning |
|---|---|
| `inference-club.com/base-path` | appended to the Service URL — almost always `/v1` |
| `inference-club.com/models` | YAML list of models (see below). Optional for engines that expose `GET /v1/models`. |
| `inference-club.com/features` | comma list, e.g. `timestamps` |
| `inference-club.com/port` | port name or number, when the Service exposes several |
| `inference-club.com/api-key-secret` | name of a Secret whose `api-key` value is sent upstream as a Bearer token. **Never uploaded to inference.club.** |

Minimal action — publish a Service that already serves `/v1` and exposes
`GET /v1/models`:

```bash
kubectl -n inference-club label svc my-vllm \
  inference-club.com/managed=true \
  inference-club.com/type=llm \
  inference-club.com/engine=vllm
kubectl -n inference-club annotate svc my-vllm inference-club.com/base-path=/v1
```

A richer Service that declares its models (recommended — model cards are never
guessed):

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
    inference-club.com/base-path: "/v1"
    inference-club.com/models: |
      - id: qwen3-30b-a3b
        hf: Qwen/Qwen3-30B-A3B
        name: Qwen3 30B A3B
        features: [reasoning, tools]
        context_length: 32768
spec:
  selector: { app: my-vllm }
  ports:
    - port: 8000
```

Within `discovery.interval` (default 30s) the provider and its models appear on
the dashboard. **For every service type, copy-paste examples, node GPU labels,
external (out-of-cluster) endpoints, and the per-service API-key pattern, send
the user to [`docs/kubernetes-discovery.md`](./docs/kubernetes-discovery.md) — do
not improvise the schema.**

---

## Step 3 — verify

- Dashboard: the provider should read **online** at
  https://inference.club/dashboard within ~1 poll interval.
- Logs:
  ```bash
  kubectl -n inference-club logs -l app.kubernetes.io/name=inference-club-agent -f
  ```
  Look for `discovered manifest from kubernetes (N services)`. `N=0` means no
  Service is labelled, or none has a Running pod behind it.
- The agent's own validator (probes each Service URL):
  ```bash
  kubectl -n inference-club exec deploy/club-agent-inference-club-agent -- host-agent doctor
  ```

## Hard rules for agents

1. **Never fabricate or guess the API key.** Ask the user; have them set it.
2. **Don't expose ports or create Ingress / public URLs** — the whole design is
   outbound-only over the tailnet. If you think you need an inbound port, you've
   misunderstood; re-read this file.
3. **Don't restart inference servers or modify the user's workloads.** The agent
   only *reads* the cluster and proxies. Labelling a Service is the one mutation
   you make, and it's reversible:
   `kubectl -n inference-club label svc <name> inference-club.com/managed-`.
4. **Use the label/annotation schema in this file verbatim.** The keys are exact
   (`inference-club.com/...`). If a key here disagrees with something you
   remember, this file and `docs/kubernetes-discovery.md` win.
5. When you change labels/annotations, tell the user it can take up to
   `discovery.interval` (default 30s) to reflect on the dashboard.
6. **Do not reintroduce Docker Compose or `agent.yaml`.** That path is gone; the
   cluster is the single source of truth.
</content>
