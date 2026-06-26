# inference-club-agent Helm chart

Run the [inference.club](https://inference.club) provider agent **inside your
k3s / Kubernetes cluster**. The agent watches the cluster for Services you've
labelled `inference-club.com/managed=true`, builds a service manifest from
them, joins inference.club's private Tailscale network, and reverse-proxies the
OpenAI-compatible `/v1/*` surface to those Services over cluster DNS.

```
inference.club  ──tailnet──▶  agent pod  ──cluster DNS──▶  your vLLM / Ollama / … Services
                              (this chart)                  (labelled inference-club.com/managed=true)
```

You never open a port or expose a public URL. The agent dials *out* to
inference.club; inference.club reaches back over the tailnet.

> **New here?** Read this chart README for installation and the values
> reference. For *which labels to put on your inference Services* (the part that
> actually makes models show up), read
> [`docs/kubernetes-discovery.md`](../../docs/kubernetes-discovery.md). If you
> are an AI assistant doing this for someone, start at
> [`AGENTS.md`](../../AGENTS.md).

---

## TL;DR

```bash
# 1. Create the namespace the agent will watch and run in.
kubectl create namespace inference-club

# 2. Store your inference.club API key (dashboard → settings → token).
kubectl -n inference-club create secret generic inference-club-api-key \
  --from-literal=api-key=ic_live_xxxxxxxxxxxxxxxxxxxxxxxx

# 3. Install the agent, pointing it at that Secret.
helm install club-agent ./charts/inference-club-agent \
  --namespace inference-club \
  --set agentName=my-home-cluster \
  --set apiKey.existingSecret=inference-club-api-key

# 4. Label an existing inference Service so the agent picks it up.
kubectl -n inference-club label svc my-vllm inference-club.com/managed=true
```

Within one poll interval (default 30s) your provider shows up **online** at
https://inference.club/dashboard and the labelled Service's models appear in the
catalog. See [`docs/kubernetes-discovery.md`](../../docs/kubernetes-discovery.md)
for the full set of labels/annotations that control model names, modalities,
features, and per-service API keys.

---

## Prerequisites

- A Kubernetes cluster you have `kubectl` + `helm` access to. k3s is the
  reference target (it ships `metrics-server` and the `ServiceLB` load
  balancer the agent expects), but any cluster works.
- At least one OpenAI-compatible inference server already running **as a
  Service in the cluster** (vLLM, Ollama, SGLang, llama.cpp, LM Studio via an
  external endpoint, …). The agent does not start inference servers — it
  publishes the ones you already run.
- An inference.club account and API key from
  https://inference.club/dashboard. (Only needed for the first registration;
  afterwards the cached tailnet identity in the agent's state is enough.)

---

## How discovery works

The agent runs one read-only poll loop against the Kubernetes API
(`AGENT_DISCOVERY=kubernetes`, set by this chart):

1. **List Services** in the watched namespace with label
   `inference-club.com/managed=true`. These *are* your service list.
2. **List Pods** to find the pod backing each Service — that tells the agent
   which node the service runs on and the exact image/command (shown on your
   public profile).
3. **List Nodes** to read GPU facts (count from `nvidia.com/gpu` allocatable;
   model/VRAM from GPU-feature-discovery labels when present).
4. Assemble a manifest, validate it, and push it to inference.club. The loop
   diffs the manifest bytes and only re-pushes on change.

The chart grants exactly the RBAC this needs: namespaced `get,list` on
`services`, `pods`, `endpointslices`, and `get` on `secrets` (for per-service
API keys); cluster-scoped `get,list` on `nodes` and `metrics.k8s.io`. See
[`templates/rbac.yaml`](./templates/rbac.yaml).

---

## Configuring the API key

Three options, in order of preference:

**1. Reference an existing Secret (recommended).** Create the Secret yourself
(so the key never lands in a values file or your shell history), then point the
chart at it:

```bash
kubectl -n inference-club create secret generic inference-club-api-key \
  --from-literal=api-key=ic_live_xxxx
```
```yaml
# values.yaml
apiKey:
  existingSecret: inference-club-api-key
  secretKey: api-key          # the key *within* the Secret (default: api-key)
```

**2. Let the chart create the Secret.** Convenient but the value passes through
Helm; avoid committing it.

```yaml
apiKey:
  value: ic_live_xxxx
```

**3. Already registered?** Once the agent has registered and you've enabled
[persistence](#persistence--tailnet-vs-direct), the cached tailnet auth key is
sufficient and the API key is no longer read. You still need it for the first
successful registration.

---

## Values reference

| key | default | description |
|---|---|---|
| `image.repository` | `ghcr.io/inference-club/inference-club-agent` | agent image |
| `image.tag` | `latest` | image tag |
| `image.pullPolicy` | `IfNotPresent` | |
| `clubUrl` | `https://inference.club` | central server; point at a dev instance to experiment |
| `agentName` | `club-host-k8s` | provider name this agent registers as (your dashboard label) |
| `apiKey.existingSecret` | `""` | name of a pre-created Secret holding the API key |
| `apiKey.secretKey` | `api-key` | key within the Secret |
| `apiKey.value` | `""` | inline key; chart renders the Secret for you (mutually exclusive with `existingSecret`) |
| `discovery.mode` | `kubernetes` | label-driven cluster discovery (leave as-is) |
| `discovery.namespace` | `""` | namespace to watch; empty = the release namespace |
| `discovery.interval` | `30s` | cluster poll interval |
| `direct.enabled` | `false` | dev only — skip Tailscale, serve plain HTTP (see below) |
| `direct.advertiseHost` | `""` | dev only — LAN host the backend uses to reach the agent (required when `direct.enabled`) |
| `listenPort` | `8090` | port the agent serves on |
| `service.type` | `LoadBalancer` | k3s ServiceLB binds node LAN IPs at `service.port` |
| `service.port` | `8090` | |
| `persistence.enabled` | `false` | persist tsnet state across restarts (recommended for tailnet mode) |
| `persistence.size` | `1Gi` | PVC size |
| `persistence.storageClass` | `""` | PVC storage class (empty = cluster default) |
| `resources` | `{}` | pod resource requests/limits |
| `nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | standard scheduling controls |

---

## Persistence & tailnet vs direct

**Tailnet mode (default, production).** The agent joins inference.club's
Tailscale network in-process via `tsnet`. Its identity (the cached auth key and
node state) lives in `/var/lib/club-host`. With `persistence.enabled=false`
that's an `emptyDir` — fine to try it out, but every pod restart forces a
re-registration. **Enable persistence for any real deployment:**

```yaml
persistence:
  enabled: true
  size: 1Gi
```

The Deployment uses the `Recreate` strategy on purpose: the agent owns a single
tailnet hostname and an RWO state volume, so two pods must never run at once.

**Direct mode (local dev only).** Set `direct.enabled=true` and
`direct.advertiseHost=<node-LAN-IP>` to skip Tailscale and serve plain HTTP on
`listenPort`. Point `clubUrl` at your local inference.club dev instance (run
with `INFERENCE_DIRECT_AGENTS=True`). This is how you develop against the *same*
discovery path as prod. In direct mode the chart also wires kubelet
readiness/liveness probes (`/healthz`); in tailnet mode liveness is checked by
the inference.club backend over the tailnet instead.

---

## Optional: live GPU stats

If your cluster runs `dcgm-exporter` (hostPort `9400`) and/or the bundled
`vram-reporter` DaemonSet (hostPort `9401`), the agent scrapes them per node for
live VRAM/utilisation shown on your dashboard. They are optional — when absent
the scrape is skipped and static GPU facts (count, model, total VRAM) still come
from node labels. Override the ports with `DCGM_SCRAPE_PORT` / `VRAM_REPORTER_PORT`
env (set to `0` to disable). See [`docs/kubernetes-discovery.md`](../../docs/kubernetes-discovery.md#optional-live-gpu-stats).

---

## Day-2

```bash
# Watch the agent come up.
kubectl -n inference-club logs -l app.kubernetes.io/name=inference-club-agent -f
# → "discovered manifest from kubernetes (N services)" then "serving on tailnet …"

# Add another service.
kubectl -n inference-club label svc another-model inference-club.com/managed=true

# Stop publishing a service (leaves the service running).
kubectl -n inference-club label svc another-model inference-club.com/managed-

# Upgrade the agent.
helm upgrade club-agent ./charts/inference-club-agent -n inference-club --reuse-values

# Uninstall (your inference Services are untouched).
helm uninstall club-agent -n inference-club
```

---

## Troubleshooting

**Provider never appears in the dashboard.** Check the logs for
`registered as provider_id=…`. A `401` means the API key Secret is wrong or
revoked. An empty-authkey error is an inference.club-side config issue.

**Logs say `discovered manifest from kubernetes (0 services)`.** No Service in
the watched namespace carries `inference-club.com/managed=true`, or the Service
has a selector but no Running pod backs it (the manifest only reports what is
actually serving). Confirm with
`kubectl -n inference-club get svc -l inference-club.com/managed=true`.

**Wrong namespace.** The agent watches `discovery.namespace` (defaulting to the
release namespace). Label Services in *that* namespace, or set
`discovery.namespace` to where your inference Services live (the chart's RBAC
Role is created in the watched namespace).

**A model shows up but with no name / modalities / context.** Those come from
the Service's annotations, not its labels — see
[`docs/kubernetes-discovery.md`](../../docs/kubernetes-discovery.md).

**Provider online but offline-after-restart.** Enable `persistence` so the
tailnet identity survives pod restarts.
</content>
