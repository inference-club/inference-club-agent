# inference-club-agent

The home-side agent for [inference.club](https://inference.club). It runs on
an always-on machine on your LAN, joins the inference.club tailnet via
embedded [`tsnet`](https://tailscale.com/kb/1244/tsnet), and reverse-proxies
an OpenAI-compatible `/v1/*` surface to a local LLM server (LM Studio,
Ollama, vLLM, llama.cpp, …).

```
inference.club  ──tailnet──▶  this agent  ──HTTP localhost──▶  vLLM / Ollama / LM Studio
                              (Go + tsnet)                     (your GPU box)
```

You do not need a Tailscale account. The agent registers with inference.club
using your account API key; the central service mints an ephemeral Tailscale
auth key for the agent and ships it back. The agent caches the key and uses
it to join the tailnet on every subsequent start.

The central service (Django + Nuxt) lives in a separate repo —
[`inference.club`](https://github.com/briancaffey/inference.club). This repo
is *only* the agent.

---

## Quick start (Docker)

1. Sign in at https://inference.club, generate an account API key.
2. Run an OpenAI-compatible LLM somewhere on your LAN (LM Studio at `:1234`
   is the typical default).
3. On the always-on machine that can reach that LLM:

   ```bash
   docker run -d --name club-host \
     -e INFERENCE_CLUB_API_KEY=ic_live_xxxxxxxxxxxxxxxxxxxxxxxx \
     -e LOCAL_LLM_URL=http://host.docker.internal:1234/v1 \
     -v club-host-state:/var/lib/club-host \
     ghcr.io/briancaffey/inference-club-agent:latest
   ```

That's it. The agent appears as an online provider in your inference.club
dashboard within a few seconds; its `/v1/models` is auto-discovered.

## Local dev (without Docker)

```bash
# build
go mod tidy
go build -o inference-club-agent .

# run against a locally-hosted inference.club
INFERENCE_CLUB_URL=http://localhost:8000 \
INFERENCE_CLUB_API_KEY=ic_live_xxxxxxxx \
LOCAL_LLM_URL=http://localhost:1234/v1 \
./inference-club-agent
```

## Env vars

| name | default | required | description |
|---|---|---|---|
| `INFERENCE_CLUB_API_KEY` | — | first run only | account-level key from inference.club/dashboard |
| `LOCAL_LLM_URL` | `http://host.docker.internal:1234/v1` | | OpenAI-compatible base URL on your LAN |
| `INFERENCE_CLUB_URL` | `https://inference.club` | | central server (override for local dev) |
| `AGENT_NAME` | — | | friendly label shown in the dashboard |
| `AGENT_HOSTNAME` | `club-host` | | tailnet hostname |
| `AGENT_STATE_DIR` | `/var/lib/club-host` | | where to cache tsnet state + the auth key |
| `AGENT_LISTEN_PORT` | `443` | | port the agent listens on inside the tailnet |
| `TAILSCALE_LOGIN_SERVER` | — | | override for self-hosted Headscale |

After the first successful registration the API key is no longer required —
the cached Tailscale auth key in `/var/lib/club-host/authkey` is sufficient.
Wipe that volume to force re-registration.

## How registration works

```
1. agent → POST https://inference.club/api/inference/agent/register/
            Authorization: Bearer <INFERENCE_CLUB_API_KEY>
            { name, tailnet_hostname, agent_port }

2. central server → mints a fresh ephemeral Tailscale auth key (tagged
   tag:club-host), creates/updates a Provider record for the user, returns:
            { provider_id, tailscale_authkey, tailnet_hostname,
              tailscale_login_server }

3. agent → caches authkey to disk, joins tailnet via tsnet, starts serving
   /v1/* on its tailnet IP.

4. central server → reaches the agent at https://<tailnet_hostname>/v1/* over
   the tailnet to fulfil chat / completion requests from end users.
```

No public URL on your end. No port forwarding. No long-lived secrets shipped
to end users — chat consumers hit `https://inference.club/v1/*` with their
own consumer API key, and inference.club routes to the right agent.

## Building the image

```bash
docker build -t inference-club-agent:dev .
```

## License

TBD.
