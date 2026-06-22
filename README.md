# inference-club-agent

**Share your home LLM with the [inference.club](https://inference.club)
community in one Docker command.**

This is the home-side agent. It runs on an always-on machine on your LAN,
joins inference.club's private Tailscale network via embedded
[`tsnet`](https://tailscale.com/kb/1244/tsnet), and reverse-proxies an
OpenAI-compatible `/v1/*` surface to whatever local LLM server you're
already running (LM Studio, Ollama, vLLM, llama.cpp, …).

```
inference.club  ──tailnet──▶  this agent  ──HTTP localhost──▶  vLLM / Ollama / LM Studio
                              (Go + tsnet)                     (your GPU box)
```

**No Tailscale account, no port forwarding, no public URL on your end.**
The agent registers with inference.club using your account API key; the
central service mints an ephemeral Tailscale auth key for you and ships
it back. It's cached on the agent so you only need the API key on first
run.

The central service (Django + Nuxt) lives in a separate repo —
[`inference.club`](https://github.com/briancaffey/inference.club).
This repo is *only* the agent.

---

## Before you start

You'll need three things:

1. **An always-on machine** with [Docker](https://docs.docker.com/get-docker/)
   installed (mac, linux, or windows-with-WSL2). Doesn't have to be the same
   box as your GPU; it just needs to reach the LLM server over the LAN.
2. **An OpenAI-compatible LLM server already running** somewhere reachable
   from that machine. Common defaults:

   | Server | Default URL |
   |---|---|
   | LM Studio | `http://host.docker.internal:1234/v1` |
   | Ollama | `http://host.docker.internal:11434/v1` |
   | vLLM | `http://host.docker.internal:8000/v1` |
   | llama.cpp `--server` | `http://host.docker.internal:8080/v1` |
3. **An inference.club account API key** — generate at
   https://inference.club/dashboard. (You only need this for the very first
   run; subsequent restarts use the cached Tailscale identity.)

> **Linux note:** `host.docker.internal` works automatically on Docker
> Desktop (Mac/Windows). On native Linux Docker, add
> `--add-host=host.docker.internal:host-gateway` to the `docker run` command,
> or point `LOCAL_LLM_URL` at your machine's LAN IP directly.

---

## Run it

> **Until the prebuilt image is published**, build it locally first:
> `docker build -t inference-club-agent:dev .` and substitute that tag
> below.

```bash
docker run -d --name club-host --restart unless-stopped \
  -e INFERENCE_CLUB_API_KEY=ic_live_xxxxxxxxxxxxxxxxxxxxxxxx \
  -e LOCAL_LLM_URL=http://host.docker.internal:1234/v1 \
  -v club-host-state:/var/lib/club-host \
  ghcr.io/briancaffey/inference-club-agent:latest
```

If you'd rather use a `docker-compose.yml`:

```yaml
services:
  club-host:
    image: ghcr.io/briancaffey/inference-club-agent:latest
    restart: unless-stopped
    environment:
      INFERENCE_CLUB_API_KEY: ${INFERENCE_CLUB_API_KEY}
      LOCAL_LLM_URL: http://host.docker.internal:1234/v1
    volumes:
      - club-host-state:/var/lib/club-host
      # Optional but recommended — see "Service manifest" below.
      - ./agent.yaml:/etc/inference-club-agent/agent.yaml:ro
volumes:
  club-host-state:
```

## Verify it's working

```bash
docker logs -f club-host
```

You should see, in order:

```
loaded cached tailscale authkey                 (or "registered as provider_id=N" on first run)
starting tsnet hostname="club-host" state="/var/lib/club-host"
serving on tailnet port 443 → http://host.docker.internal:1234/v1
```

Then open https://inference.club/dashboard — your provider appears as
**online** and its `/v1/models` is auto-discovered.

Quick local sanity check that the LLM is reachable from inside the
container:

```bash
docker exec club-host wget -qO- http://host.docker.internal:1234/v1/models
```

---

## Service manifest

The manifest is a YAML file describing the hosts in your home network,
each host's GPU, and the LLM services running on each host. The agent
uploads it to inference.club on startup, so your public profile at
`inference.club/<your-github-handle>` renders the same picture you
write here.

Start from [`agent.yaml.example`](./agent.yaml.example):

```bash
cp agent.yaml.example agent.yaml
$EDITOR agent.yaml
```

Mount it into the container at `/etc/inference-club-agent/agent.yaml`
(the docker-compose snippet above already does this). After editing,
reload the agent without restarting:

```bash
docker kill -s HUP club-host
```

### Declaring model capabilities

inference.club shows each model's modalities, features, and context window in
its catalog and playground. **You declare these per model — they are never
guessed.** On each model under a service, `id` is the only required field
(`hf` is strongly recommended so the same model pools across nodes and links
its HuggingFace page); everything else is optional:

| field | example | notes |
|-------|---------|-------|
| `name` | `Qwen3 30B A3B` | human-friendly display name |
| `input_modalities` | `[text, image]` | defaults from the service `type` when omitted |
| `output_modalities` | `[text]` | defaults from the service `type` when omitted |
| `features` | `[reasoning, tools]` | model capabilities surfaced as badges |
| `context_length` | `32768` | declared ceiling; the live-probed window wins when known |
| `quantization` | `fp8` | per-deployment |

```yaml
models:
  - id: qwen3-30b-a3b
    hf: Qwen/Qwen3-30B-A3B
    name: Qwen3 30B A3B
    features: [reasoning, tools]
    context_length: 32768
    quantization: fp8
```

Modalities default from the service `type` (`llm`→text/text, `stt`→audio/text,
`tts`→text/audio, `image`→[text,image]/image), so a plain text LLM needs no
modality fields at all.

Validate the manifest and probe each service URL without restarting:

```bash
docker exec club-host host-agent doctor
```

If you don't provide a manifest, the agent falls back to the legacy
single-LLM behaviour driven by `LOCAL_LLM_URL` and `AGENT_NAME` — your
existing `docker run` keeps working unchanged.

---

## Env vars

| name | default | required | description |
|---|---|---|---|
| `INFERENCE_CLUB_API_KEY` | — | **first run only** | account-level key from https://inference.club/dashboard |
| `LOCAL_LLM_URL` | `http://host.docker.internal:1234/v1` |  | OpenAI-compatible base URL on your LAN |
| `INFERENCE_CLUB_URL` | `https://inference.club` |  | central server (override for local dev) |
| `AGENT_NAME` | — |  | friendly label shown in the dashboard |
| `AGENT_HOSTNAME` | `club-host` |  | tailnet hostname |
| `AGENT_STATE_DIR` | `/var/lib/club-host` |  | where to cache tsnet state + the auth key |
| `AGENT_LISTEN_PORT` | `443` (`8080` in direct mode) |  | port the agent listens on |
| `AGENT_CONFIG_FILE` | `/etc/inference-club-agent/agent.yaml` |  | path to the service manifest |
| `TAILSCALE_LOGIN_SERVER` | — |  | override for self-hosted [Headscale](https://github.com/juanfont/headscale) |
| `AGENT_DIRECT` | `false` |  | **dev only** — skip Tailscale, serve plain HTTP on a TCP port (see [Local development](#local-development)) |
| `AGENT_ADVERTISE_HOST` | `host.docker.internal` |  | **dev only** — host the dev server uses to reach this agent in direct mode |

After registration the API key is no longer used — the cached Tailscale
auth key in `${AGENT_STATE_DIR}/authkey` is sufficient. Wipe the volume
(`docker volume rm club-host-state`) to force re-registration.

---

## Day-2 operations

### Switch to a different local LLM

```bash
docker stop club-host && docker rm club-host
docker run -d --name club-host --restart unless-stopped \
  -e LOCAL_LLM_URL=http://host.docker.internal:11434/v1 \
  -v club-host-state:/var/lib/club-host \
  ghcr.io/briancaffey/inference-club-agent:latest
# (no API key needed — re-uses the cached identity)
```

### Re-discover models on the central server

In the inference.club dashboard, hit **Refresh models** on your provider —
the central server probes your agent at `https://<tailnet_hostname>/v1/models`
and updates the list.

### Force a fresh registration (e.g. moved to a different account)

```bash
docker stop club-host && docker rm club-host
docker volume rm club-host-state
# then run with a fresh INFERENCE_CLUB_API_KEY
```

### Upgrade

```bash
docker pull ghcr.io/briancaffey/inference-club-agent:latest
docker stop club-host && docker rm club-host
# re-run the same `docker run` command — volume is preserved
```

---

## Troubleshooting

**`registration failed: ... 401 Unauthorized`** — your `INFERENCE_CLUB_API_KEY`
is wrong or revoked. Generate a new one at https://inference.club/dashboard.

**`registration failed: ... empty tailscale_authkey`** — the central server
doesn't have its Tailscale OAuth client configured yet. Not your fault —
ping inference.club support.

**`tsnet up: ...`** — Tailscale couldn't establish the tunnel. Check that
outbound UDP/443 isn't blocked by a firewall.

**`upstream error: dial tcp host.docker.internal:1234: connect: connection refused`** —
the agent can't reach your local LLM. On Linux, make sure you used
`--add-host=host.docker.internal:host-gateway`. Or just use the LAN IP.

**Provider stays "offline" in the dashboard** — `docker logs club-host` and
look for the `serving on tailnet port 443` line. If it never appears, the
tailnet join failed (see above). If it does appear but inference.club still
says offline, the central server can't reach your agent over the tailnet —
inference.club issue.

---

## Local development

If your inference runs in a Kubernetes cluster, develop against the **exact same
discovery path as prod** — no static `agent.yaml`. Run a second copy of the agent
**inside the cluster** with `AGENT_DISCOVERY=kubernetes` (so it builds its manifest
from the labeled Services, like the prod agent) but in *direct mode*
(`AGENT_DIRECT=1`) pointed at your local inference.club:

```
your browser ─▶ inference.club (dev, :8101) ─▶ agent-dev (in-cluster, direct) ─▶ Services (ClusterIP)
                INFERENCE_DIRECT_AGENTS=True     AGENT_DISCOVERY=kubernetes        (same as prod)
```

Because k8s discovery resolves cluster-internal Service DNS, the agent must run
*in* the cluster; it advertises a LAN address (its node IP + a hostPort) that your
local backend reaches, and the backend runs with `INFERENCE_DIRECT_AGENTS=True` to
trust it. A ready-made manifest lives with the cluster config at
`home-cluster/services/agent-dev/agent-dev.yaml` — set two values for your machine:

- `INFERENCE_CLUB_URL` → `http://<your-LAN-IP>:8101` (reserve the IP in DHCP)
- `AGENT_ADVERTISE_HOST` → the LAN IP of the node it's pinned to

```bash
kubectl apply -f agent-dev.yaml
kubectl logs -n inference-club -l app=agent-dev -f   # → "discovered manifest from kubernetes (…)"
```

Your local `/v1/models` then populates from the cluster exactly as prod does. The
agent's file-manifest mode (`agent.yaml`) still exists in the binary for standalone
(non-Kubernetes) use — see "Service manifest" above — but the cluster is the single
source of truth for both local and prod.

---

## How registration works under the hood

```
1. agent → POST https://inference.club/api/inference/agent/register/
            Authorization: Bearer <INFERENCE_CLUB_API_KEY>
            { name, tailnet_hostname, agent_port }

2. central server → mints a fresh ephemeral Tailscale auth key (tag:club-host),
                    creates/updates the Provider record, returns:
            { provider_id, tailscale_authkey, tailnet_hostname,
              tailscale_login_server }

3. agent → caches authkey to disk, joins the tailnet via tsnet,
           starts serving /v1/* on its tailnet IP.

4. central server → reaches the agent at https://<tailnet_hostname>/v1/* over
                    the tailnet to fulfil chat / completion requests from
                    end users.
```

No public URL on your end. No port forwarding. End users hit
`https://inference.club/v1/*` with their own consumer API keys; the central
server routes to the right agent.

---

## License

TBD.
