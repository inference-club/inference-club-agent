# Deployment environments — where the agent runs and how it reaches a backend

One agent binary, several deployment topologies. The agent always does the same
two jobs — **discover** the inference Services it should advertise, and
**register** them with an inference.club backend so requests can be routed to
them. What changes between environments is only *which backend* it talks to and
*how the backend reaches the agent back*.

This guide covers the four topologies in use and how to pick one. For labelling
the Services themselves, see [kubernetes-discovery.md](./kubernetes-discovery.md).

## The one decision: tailnet vs. direct

The agent's `/v1` proxy has to be reachable *by the backend* (that's how a user's
request gets from inference.club to your GPU). There are two ways:

| Mode | `AGENT_DIRECT` | How the backend reaches the agent | Use when |
|------|----------------|-----------------------------------|----------|
| **Tailnet** | unset | The agent joins the inference.club tailnet; the backend dials it by its tailnet address. | The agent and backend are on **different networks** (e.g. your GPU box ↔ the hosted cloud). |
| **Direct** | `1` | The agent advertises an address (`AGENT_ADVERTISE_HOST:AGENT_LISTEN_PORT`) that the backend can already route to. | The agent and backend share a network — same LAN, or the **same cluster**. |

Direct mode requires the **backend** to run with `INFERENCE_DIRECT_AGENTS=True`,
so it trusts the advertised address instead of expecting a tailnet host.

## The four topologies

### 1. Cloud / tailnet (production)
The agent runs on a contributor's GPU machine anywhere on the internet and
registers to the hosted backend. It joins the tailnet and is reached over it.

```
AGENT_DISCOVERY=kubernetes        # or file, for non-k8s hosts
INFERENCE_CLUB_URL=https://api.inference.club
INFERENCE_CLUB_API_KEY=<token>
AGENT_NAME=club-host              # the provider name shown in the dashboard
# AGENT_DIRECT unset → tailnet mode
```
Install via the Helm chart (`charts/`). This is the community/production path.

### 2. Direct to a laptop backend (local development)
The agent runs in your k3s cluster but registers to an inference.club backend
running in Docker Compose on your Mac. It advertises a **LAN address** (a pinned
node's IP + a `hostPort`) that the Mac can reach.

```
AGENT_DIRECT=1
AGENT_ADVERTISE_HOST=192.168.5.173   # LAN IP of the node it's pinned to
AGENT_LISTEN_PORT=8091               # published as a hostPort
INFERENCE_CLUB_URL=http://192.168.6.12:8101   # your Mac's LAN IP + compose port
```
Backend (compose) must set `INFERENCE_DIRECT_AGENTS=True`. Reference manifest:
`home-cluster/services/agent-dev/agent-dev.yaml`.

### 3. Fully in-cluster (home-lab / self-hosted)
The agent, the backend, **and** the inference Services all run in the same k3s
cluster (e.g. inference.club self-hosted at `club.lan`). Everything stays on
cluster DNS — no tailnet, no LAN IPs. The agent advertises its **own Service
DNS** so the backend reaches its proxy in-cluster.

```
AGENT_DIRECT=1
AGENT_ADVERTISE_HOST=agent-club-lan.inference-club.svc.cluster.local
AGENT_LISTEN_PORT=8091               # a Service maps this to the pod
INFERENCE_CLUB_URL=http://backend.inference-club-web.svc.cluster.local:8001
AGENT_NAME=home-cluster
```
Backend (its ConfigMap) must set `INFERENCE_DIRECT_AGENTS=True`, and its
`DJANGO_ALLOWED_HOSTS` must include the Service name the agent uses in
`INFERENCE_CLUB_URL` (e.g. `backend.inference-club-web.svc.cluster.local`).
Reference manifest: `home-cluster/services/agent-club-lan/agent-club-lan.yaml`.
This topology needs **no external network** — a laptop, a cloud, or a tailnet.

### 4. Standalone Docker + file manifest (no Kubernetes)
For a single box without k3s: run the agent container with a static
`agent.yaml` (`AGENT_DISCOVERY=file`) instead of Kubernetes discovery. See
`docker-compose.yml` + `agent.yaml.example`. Pairs with tailnet mode for the
hosted backend.

## Naming conventions

- **`AGENT_NAME`** is the **provider name** shown in the dashboard and used to
  identify this node. Pick one that reads well for the environment:
  - `club-host` — the canonical provider registering to the hosted/dev backend.
  - `home-cluster` — a self-hosted cluster serving its own `club.lan` instance.
  - Anything descriptive for a specific box (`workshop-4090`, `garage-rig`, …).
- The name is **per-backend**: the same `AGENT_NAME` on two different backends is
  two independent providers, so reuse is fine across environments.
- **Namespaces**: the agent watches `AGENT_DISCOVERY_NAMESPACE` (default the
  agent's own namespace) for labelled Services. Run several agents in one cluster
  (one per backend — prod, dev, home-lab) all watching the *same* services
  namespace; they reuse one ServiceAccount's RBAC.
- **Secrets**: never put `INFERENCE_CLUB_API_KEY` in a values file — use a k8s
  Secret and `secretKeyRef` (all reference manifests do).

## Environment variable reference

| Variable | Purpose | Default |
|----------|---------|---------|
| `INFERENCE_CLUB_URL` | Backend to register with | required |
| `INFERENCE_CLUB_API_KEY` | A full-member API token on that backend | required |
| `AGENT_NAME` | Provider name (see conventions) | `club-host` |
| `AGENT_DISCOVERY` | `kubernetes` or `file` | `file` |
| `AGENT_DISCOVERY_NAMESPACE` | Namespace to watch for Services | agent's namespace |
| `AGENT_DISCOVERY_INTERVAL` | Poll interval | `30s` |
| `AGENT_DIRECT` | `1` = direct mode (advertise an address); unset = tailnet | unset |
| `AGENT_ADVERTISE_HOST` | Address the backend dials (direct mode) | — |
| `AGENT_LISTEN_PORT` | Port the `/v1` proxy binds | `8091` |
| `AGENT_STATE_DIR` | Where the agent keeps local state | — |
| `AGENT_HEARTBEAT_INTERVAL` | Re-register / liveness cadence | — |
| `TAILSCALE_LOGIN_SERVER` | Custom control server (tailnet mode) | — |

> Backend side: direct-mode agents require `INFERENCE_DIRECT_AGENTS=True` on the
> inference.club backend, and the backend's `DJANGO_ALLOWED_HOSTS` must accept the
> host in `INFERENCE_CLUB_URL`.

## Checklist for a new environment
1. Create a full-member API token on the target backend; store it as a Secret.
2. Decide tailnet vs. direct (are the agent and backend on the same network?).
3. If direct: set `INFERENCE_DIRECT_AGENTS=True` on the backend and pick an
   `AGENT_ADVERTISE_HOST` the backend can reach.
4. Point `INFERENCE_CLUB_URL` at the backend; set a clear `AGENT_NAME`.
5. Apply; watch for `discovered manifest …` + `registered …` + `manifest uploaded
   (status 200)` in the logs, then `GET /v1/models` on the backend.
