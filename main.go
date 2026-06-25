// inference-club-agent runs on a club member's home machine. It registers
// itself with the inference.club central service, joins inference.club's
// tailnet via embedded tsnet, and reverse-proxies an OpenAI-compatible
// /v1/* surface to a local LLM server (LM Studio, Ollama, vLLM, llama.cpp).
//
// Lifecycle:
//
//  1. On first run, if no Tailscale auth key is cached on disk, POST to
//     {INFERENCE_CLUB_URL}/api/inference/agent/register/ with the user's
//     INFERENCE_CLUB_API_KEY as a Bearer token. The central service mints
//     a fresh ephemeral Tailscale auth key tagged tag:club-host and
//     returns it. We persist it.
//  2. Start tsnet with the auth key, listen on :443 inside the tailnet.
//  3. Reverse-proxy /v1/* to LOCAL_LLM_URL (default: LM Studio on :1234).
//
// Env vars:
//
//	INFERENCE_CLUB_API_KEY   account-level key from https://inference.club/dashboard
//	INFERENCE_CLUB_URL       central server (default: https://inference.club)
//	LOCAL_LLM_URL            e.g. http://host.docker.internal:1234/v1 (used only when no manifest)
//	AGENT_NAME               friendly name shown in the inference.club UI (used only when no manifest)
//	AGENT_HOSTNAME           tailnet hostname (default: club-host-<rand>)
//	AGENT_STATE_DIR          where to cache tsnet state + authkey (default: /var/lib/club-host)
//	AGENT_LISTEN_PORT        port to listen on (tailnet mode default: 443; direct mode default: 8080)
//	AGENT_CONFIG_FILE        path to agent.yaml (default: /etc/inference-club-agent/agent.yaml)
//	TAILSCALE_LOGIN_SERVER   override for Headscale (default: empty → Tailscale's control plane)
//
// Kubernetes discovery mode — the cluster replaces agent.yaml (see
// internal/discovery):
//
//	AGENT_DISCOVERY           "kubernetes" to build the manifest from labeled
//	                          Services instead of agent.yaml; "static"/"" = file
//	AGENT_DISCOVERY_NAMESPACE namespace to watch (default: inference-club)
//	AGENT_DISCOVERY_INTERVAL  cluster poll interval (default: 30s)
//
// Local-dev (no Tailscale) mode — see README "Local development":
//
//	AGENT_DIRECT             when truthy, skip Tailscale entirely: serve plain HTTP on a
//	                         TCP port and tell the server to reach the agent directly.
//	AGENT_ADVERTISE_HOST     host the dev server uses to reach this agent in direct mode
//	                         (default: host.docker.internal). Reported to register as the
//	                         provider's reachable hostname.
//
// Subcommands:
//
//	doctor   load + validate the manifest, probe each service URL,
//	         print actionable diagnostics, exit non-zero on failure.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/discovery"
	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
	"github.com/briancaffey/inference-club-agent/host-agent/internal/router"
	"tailscale.com/tsnet"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "doctor":
			os.Exit(runDoctor())
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "usage: inference-club-agent [doctor]")
			os.Exit(0)
		}
	}

	cfg := loadConfig()

	// kubernetes discovery mode: the cluster, not agent.yaml, is the source
	// of truth. Build the manifest from labeled Services and keep rebuilding
	// on a poll loop (below) so the manifest tracks the cluster.
	var kdisc *discovery.Kubernetes
	if cfg.Discovery == "kubernetes" {
		var err error
		kdisc, err = discovery.NewInCluster(
			getenv("AGENT_NAME", "club-host"), cfg.DiscoveryNamespace)
		if err != nil {
			log.Fatalf("kubernetes discovery: %v", err)
		}
	} else if cfg.Discovery != "" && cfg.Discovery != "static" {
		log.Fatalf("AGENT_DISCOVERY must be empty, \"static\" or \"kubernetes\", got %q", cfg.Discovery)
	}

	mf, mfErrs := loadOrDiscoverManifest(cfg, kdisc)
	if mfErrs != nil {
		log.Fatalf("manifest invalid — fix and restart:\n  - %s",
			strings.Join(mfErrs, "\n  - "))
	}
	// The manifest is the source of truth when present: agent.name and
	// agent.hostname/listen_port from YAML override env-var equivalents,
	// so the manifest the operator uploads describes the same identity
	// that registered with inference.club.
	applyManifestToConfig(cfg, mf)

	if cfg.Direct {
		// Direct (no-Tailscale) mode: advertise a routable host:port the
		// dev server can reach directly (host.docker.internal:<port> by
		// default). The manifest's tailnet identity (agent.hostname /
		// agent.listen_port) is deliberately ignored for addressing —
		// those describe the tailnet listener, which we don't use here.
		cfg.Hostname = cfg.AdvertiseHost
		cfg.ListenPort = directListenPort()
		if err := registerDirect(cfg); err != nil {
			log.Fatalf("registration failed: %v", err)
		}
	} else {
		if err := ensureAuthKey(cfg); err != nil {
			log.Fatalf("registration failed: %v", err)
		}
	}

	pushManifest(cfg, mf)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	target, err := url.Parse(cfg.LocalLLMURL)
	if err != nil {
		log.Fatalf("invalid LOCAL_LLM_URL: %v", err)
	}
	var routerHolder atomic.Pointer[router.Router]
	routerHolder.Store(buildRouter(mf.Manifest, target))
	logRouterBackends("initial", routerHolder.Load(), cfg.LocalLLMURL)

	// reload re-derives the manifest (file or cluster), re-uploads it, and
	// atomically swaps the router. In-flight requests keep using the previous
	// router (their goroutine already holds the *Router pointer); new
	// requests pick up the swap. We do not touch the running tsnet listener.
	// In kubernetes mode `force` is false on poll ticks so an unchanged
	// cluster costs nothing (byte-diff of the built YAML, no push, no probe).
	lastRaw := mf.Raw
	var reloadMu sync.Mutex
	reload := func(why string, force bool) {
		reloadMu.Lock()
		defer reloadMu.Unlock()
		newMF, errs := loadOrDiscoverManifest(cfg, kdisc)
		if errs != nil {
			log.Printf("manifest reload (%s) failed (keeping previous):\n  - %s",
				why, strings.Join(errs, "\n  - "))
			return
		}
		if !force && lastRaw != nil && bytes.Equal(newMF.Raw, lastRaw) {
			return
		}
		lastRaw = newMF.Raw
		log.Printf("manifest changed (%s) — re-uploading and rebuilding router", why)
		pushManifest(cfg, newMF)
		routerHolder.Store(buildRouter(newMF.Manifest, target))
		logRouterBackends("reloaded", routerHolder.Load(), cfg.LocalLLMURL)
	}

	// SIGHUP → immediate forced reload (file mode: re-read agent.yaml;
	// kubernetes mode: re-list the cluster right now).
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			log.Print("SIGHUP received — reloading manifest")
			reload("SIGHUP", true)
		}
	}()

	// kubernetes mode: poll the cluster so Services coming and going are
	// reflected without anyone sending signals.
	if kdisc != nil {
		go func() {
			tick := time.NewTicker(cfg.DiscoveryInterval)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					reload("poll", false)
				}
			}
		}()
	}

	// Liveness beacon (phase 1): reach OUT to inference.club on an interval so
	// the provider stays "online" as long as the agent is alive — independent
	// of discovery mode, manifest churn, and whether the backend can reach us.
	// register() already bumped last_seen once; this keeps it warm.
	if cfg.HeartbeatInterval > 0 {
		go func() {
			tick := time.NewTicker(cfg.HeartbeatInterval)
			defer tick.Stop()
			var failures int
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if err := sendHeartbeat(cfg); err != nil {
						// Log the first failure then ~every 10th, so a flaky
						// link doesn't flood the journal.
						if failures%10 == 0 {
							log.Printf("heartbeat failed: %v", err)
						}
						failures++
					} else {
						failures = 0
					}
				}
			}
		}()
		log.Printf("liveness beacon: every %s → %s", cfg.HeartbeatInterval, cfg.BaseURL)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		routerHolder.Load().ServeHTTP(w, r)
	})
	// Live cluster snapshot (PRD 07). Only meaningful in kubernetes
	// discovery mode — a static-manifest agent has no cluster to report.
	mux.HandleFunc("/cluster/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if kdisc == nil {
			http.Error(w, "cluster state requires kubernetes discovery mode", http.StatusNotFound)
			return
		}
		stateCtx, stateCancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer stateCancel()
		state, err := kdisc.ClusterState(stateCtx)
		if err != nil {
			log.Printf("cluster state: %v", err)
			http.Error(w, "cluster state unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found — try /v1/models", http.StatusNotFound)
	})

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	// The listener depends on the mode. Direct mode serves plain HTTP on a
	// TCP port with no Tailscale involved; tailnet mode brings up tsnet and
	// listens on the tailnet interface.
	var listener net.Listener
	if cfg.Direct {
		listener, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.ListenPort))
		if err != nil {
			log.Fatalf("listen: %v", err)
		}
		log.Printf("serving (direct mode, no tailscale) on :%d → %s", cfg.ListenPort, cfg.LocalLLMURL)
	} else {
		srv := &tsnet.Server{
			Hostname:  cfg.Hostname,
			Dir:       cfg.StateDir,
			AuthKey:   cfg.AuthKey,
			Ephemeral: false,
		}
		if cfg.LoginServer != "" {
			srv.ControlURL = cfg.LoginServer
		}
		defer srv.Close()

		log.Printf("starting tsnet hostname=%q state=%q", cfg.Hostname, cfg.StateDir)
		if _, err := srv.Up(ctx); err != nil {
			log.Fatalf("tsnet up: %v", err)
		}
		// Record the node's real tailnet IP and report it right away. Tailscale
		// may have uniquified our hostname (club-host-1 → club-host-1-1) if a
		// stale device still held the name, so the canonical hostname the
		// backend has on file may not resolve — but the IP always routes. The
		// immediate heartbeat closes the gap so the backend repoints within
		// seconds of a restart instead of waiting a full beacon interval.
		if ip4, _ := srv.TailscaleIPs(); ip4.IsValid() {
			cfg.mu.Lock()
			cfg.TailnetIP = ip4.String()
			cfg.mu.Unlock()
			log.Printf("tailnet identity: ip=%s (hostname=%q)", ip4, cfg.Hostname)
			go func() {
				if err := sendHeartbeat(cfg); err != nil {
					log.Printf("initial identity heartbeat failed (next beacon will retry): %v", err)
				}
			}()
		} else {
			log.Printf("warn: tsnet reported no tailnet IP; backend will fall back to hostname %q", cfg.Hostname)
		}
		listener, err = srv.Listen("tcp", fmt.Sprintf(":%d", cfg.ListenPort))
		if err != nil {
			log.Fatalf("listen: %v", err)
		}
		log.Printf("serving on tailnet port %d → %s", cfg.ListenPort, cfg.LocalLLMURL)
	}
	defer listener.Close()

	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

// buildRouter probes each upstream for its served context window (best-effort,
// short timeout) and constructs the model→backend router with that data baked
// into /v1/models. Called at startup and on every SIGHUP reload.
func buildRouter(m *manifest.Manifest, fallback *url.URL) *router.Router {
	ctxLens := router.ProbeContextLengths(m, 4*time.Second)
	return router.NewWithProbe(m, fallback, ctxLens)
}

// logRouterBackends prints the per-backend / per-model routing the agent
// will use, so an operator restarting the container or sending SIGHUP
// can confirm the manifest landed the way they expected.
func logRouterBackends(when string, r *router.Router, fallback string) {
	backends := r.Backends()
	if len(backends) == 0 {
		log.Printf("router (%s): no manifest backends — all /v1/* falls back to %s", when, fallback)
		return
	}
	log.Printf("router (%s): %d backend(s); unknown models fall back to %s", when, len(backends), fallback)
	for _, b := range backends {
		log.Printf("  backend %s", b)
	}
}

type config struct {
	BaseURL     string
	APIKey      string
	LocalLLMURL string
	Name        string
	Hostname    string
	StateDir    string
	ListenPort  int
	AuthKey     string
	LoginServer string
	ConfigFile  string

	// Direct (no-Tailscale) local-dev mode.
	Direct        bool
	AdvertiseHost string

	// Discovery selects where the manifest comes from: "" / "static" reads
	// AGENT_CONFIG_FILE (agent.yaml); "kubernetes" builds it from Services
	// labeled inference-club.com/managed=true in DiscoveryNamespace.
	Discovery          string
	DiscoveryNamespace string
	DiscoveryInterval  time.Duration

	// How often the agent beacons inference.club to stay "online". Outbound,
	// so it works behind NAT without the backend reaching back in. See
	// sendHeartbeat. 0 disables the beacon (falls back to register + the
	// backend's inbound /healthz probe, the old behavior).
	HeartbeatInterval time.Duration

	// TailnetIP is this node's actual tailnet IPv4, learned from tsnet after
	// joining. Reported on every heartbeat so the backend dials the live node
	// directly over WireGuard — immune to Tailscale renaming the node on rejoin
	// (club-host-1 → club-host-1-1), which the canonical-hostname dial can't
	// survive. Empty in direct mode and until the node is up.
	TailnetIP string

	mu sync.Mutex // guards Name/Hostname/ListenPort/TailnetIP under SIGHUP reload
}

func loadConfig() *config {
	c := &config{
		BaseURL:     getenv("INFERENCE_CLUB_URL", "https://inference.club"),
		APIKey:      getenv("INFERENCE_CLUB_API_KEY", ""),
		LocalLLMURL: getenv("LOCAL_LLM_URL", "http://host.docker.internal:1234/v1"),
		Name:        getenv("AGENT_NAME", ""),
		Hostname:    getenv("AGENT_HOSTNAME", "club-host"),
		StateDir:    getenv("AGENT_STATE_DIR", "/var/lib/club-host"),
		ListenPort:  443,
		LoginServer: getenv("TAILSCALE_LOGIN_SERVER", ""),
		ConfigFile:  getenv("AGENT_CONFIG_FILE", "/etc/inference-club-agent/agent.yaml"),

		Direct:        getenvBool("AGENT_DIRECT"),
		AdvertiseHost: getenv("AGENT_ADVERTISE_HOST", "host.docker.internal"),

		Discovery:          strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_DISCOVERY"))),
		DiscoveryNamespace: getenv("AGENT_DISCOVERY_NAMESPACE", "inference-club"),
		DiscoveryInterval:  30 * time.Second,
		HeartbeatInterval:  30 * time.Second,
	}
	if v := os.Getenv("AGENT_DISCOVERY_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			log.Fatalf("invalid AGENT_DISCOVERY_INTERVAL (want e.g. 30s): %q", v)
		}
		c.DiscoveryInterval = d
	}
	// AGENT_HEARTBEAT_INTERVAL tunes the liveness beacon; "0"/"off" disables it.
	if v := os.Getenv("AGENT_HEARTBEAT_INTERVAL"); v != "" {
		if v == "0" || strings.EqualFold(v, "off") {
			c.HeartbeatInterval = 0
		} else {
			d, err := time.ParseDuration(v)
			if err != nil || d < time.Second {
				log.Fatalf("invalid AGENT_HEARTBEAT_INTERVAL (want e.g. 30s, or 0 to disable): %q", v)
			}
			c.HeartbeatInterval = d
		}
	}
	if v := os.Getenv("AGENT_LISTEN_PORT"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &c.ListenPort); err != nil {
			log.Fatalf("invalid AGENT_LISTEN_PORT: %v", err)
		}
	}
	return c
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// directListenPort is the TCP port the agent serves on in direct (no-Tailscale)
// mode. AGENT_LISTEN_PORT overrides; otherwise 8080. The manifest's
// agent.listen_port is intentionally ignored here — it describes the tailnet
// listener, which direct mode doesn't use.
func directListenPort() int {
	v := os.Getenv("AGENT_LISTEN_PORT")
	if v == "" {
		return 8080
	}
	var p int
	if _, err := fmt.Sscanf(v, "%d", &p); err != nil || p <= 0 || p > 65535 {
		log.Fatalf("invalid AGENT_LISTEN_PORT: %q", v)
	}
	return p
}

// registerDirect handles registration in direct (no-Tailscale) mode. It calls
// the same /agent/register/ endpoint so the server creates/updates the
// Provider and records the directly-reachable address the agent reports, but
// it neither needs nor uses a Tailscale auth key. An API key is required — the
// dev server must know which user this provider belongs to.
func registerDirect(c *config) error {
	if c.APIKey == "" {
		return errors.New("AGENT_DIRECT is set but INFERENCE_CLUB_API_KEY is empty — " +
			"create a user and API token on your dev inference.club and pass it so the agent can register")
	}
	resp, err := register(c)
	if err != nil {
		return err
	}
	// The dev server (INFERENCE_DIRECT_AGENTS=True) echoes back the address
	// it stored; trust it so both sides agree on how the agent is reached.
	if resp.TailnetHostname != "" {
		c.Hostname = resp.TailnetHostname
	}
	log.Printf("registered (direct mode) as provider_id=%d reachable at http://%s:%d/v1",
		resp.ProviderID, c.Hostname, c.ListenPort)
	return nil
}

// ensureAuthKey loads a cached Tailscale auth key from disk, or registers
// with inference.club to fetch one.
func ensureAuthKey(c *config) error {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	keyPath := filepath.Join(c.StateDir, "authkey")

	// Always re-register when the API key is available. Register is
	// idempotent on the server (update_or_create on user+name) and the
	// response carries the canonical tailnet hostname the server has
	// recorded for this Provider — e.g. "club-host-1". Without this,
	// the agent's tsnet identity drifts on restart: cfg.Hostname comes
	// from env/YAML ("club-host"), tsnet renames the node away from
	// "club-host-1", and the backend can no longer reach the agent
	// because its SOCKS proxy can't resolve the now-stale name. (Bug
	// hidden because last_seen_at was last bumped at first registration.)
	if c.APIKey != "" {
		resp, err := register(c)
		if err == nil && resp.TailscaleAuthkey != "" {
			if werr := os.WriteFile(keyPath, []byte(resp.TailscaleAuthkey), 0o600); werr != nil {
				log.Printf("warn: write authkey: %v", werr)
			}
			c.AuthKey = resp.TailscaleAuthkey
			if resp.TailscaleLoginServer != "" {
				c.LoginServer = resp.TailscaleLoginServer
			}
			if resp.TailnetHostname != "" {
				c.Hostname = resp.TailnetHostname
			}
			log.Printf("registered as provider_id=%d tailnet_hostname=%s",
				resp.ProviderID, c.Hostname)
			return nil
		}
		log.Printf("register failed (%v) — falling back to cached authkey", err)
	}

	if data, err := os.ReadFile(keyPath); err == nil && len(bytes.TrimSpace(data)) > 0 {
		c.AuthKey = string(bytes.TrimSpace(data))
		log.Print("loaded cached tailscale authkey (no API key for re-registration; " +
			"if the agent appears offline upstream, set INFERENCE_CLUB_API_KEY and " +
			"restart so we can refetch the canonical tailnet hostname)")
		return nil
	}

	return errors.New("no cached authkey and INFERENCE_CLUB_API_KEY not set — generate one at https://inference.club/dashboard and pass it via env")
}

type registerResponse struct {
	ProviderID           int    `json:"provider_id"`
	TailscaleAuthkey     string `json:"tailscale_authkey"`
	TailscaleLoginServer string `json:"tailscale_login_server"`
	TailnetHostname      string `json:"tailnet_hostname"`
}

// loadOrDiscoverManifest returns the manifest from whichever source the
// config selects: the kubernetes discoverer when one was constructed, else
// agent.yaml / env synthesis via loadManifest.
func loadOrDiscoverManifest(cfg *config, kdisc *discovery.Kubernetes) (*manifest.LoadResult, []string) {
	if kdisc == nil {
		return loadManifest(cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lr, verrs, err := kdisc.Build(ctx)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if verrs != nil {
		return nil, verrs
	}
	hosts, services := 0, 0
	for _, h := range lr.Manifest.Hosts {
		hosts++
		services += len(h.Services)
	}
	log.Printf("discovered manifest from kubernetes (%d hosts, %d services)", hosts, services)
	return lr, nil
}

// loadManifest reads agent.yaml from cfg.ConfigFile. If the file is
// absent, it synthesizes a one-host / one-service manifest from the
// pre-YAML env vars (back-compat path so existing single-LLM users keep
// working with zero config changes).
//
// Returns (manifest, validationErrors). A nil error slice means the
// manifest is valid; a non-nil slice means caller must decide whether
// to abort (startup) or keep the previous manifest (reload).
func loadManifest(cfg *config) (*manifest.LoadResult, []string) {
	if _, err := os.Stat(cfg.ConfigFile); errors.Is(err, os.ErrNotExist) {
		log.Printf("no manifest at %s — synthesizing from env vars", cfg.ConfigFile)
		m := manifest.SynthesizeFromEnv(
			os.Getenv("AGENT_NAME"),
			os.Getenv("AGENT_LISTEN_PORT"),
			cfg.LocalLLMURL,
		)
		errs := manifest.Validate(m)
		if errs != nil {
			return nil, errs
		}
		return &manifest.LoadResult{Manifest: m, Raw: nil}, nil
	}

	lr, errs, err := manifest.Load(cfg.ConfigFile)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if errs != nil {
		return nil, errs
	}
	log.Printf("loaded manifest from %s (%d hosts)", cfg.ConfigFile, len(lr.Manifest.Hosts))
	return lr, nil
}

// applyManifestToConfig lets the manifest override the env-var-derived
// config so registration uses the operator's chosen identity.
func applyManifestToConfig(cfg *config, lr *manifest.LoadResult) {
	if lr == nil || lr.Manifest == nil {
		return
	}
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	if name := strings.TrimSpace(lr.Manifest.Agent.Name); name != "" {
		cfg.Name = name
	}
	if h := strings.TrimSpace(lr.Manifest.Agent.Hostname); h != "" {
		cfg.Hostname = h
	}
	if p := lr.Manifest.Agent.ListenPort; p > 0 && p <= 65535 {
		cfg.ListenPort = p
	}
}

// pushManifest uploads the manifest to inference.club. Errors are logged
// and ignored — a failed upload should not take down the agent (the
// router still works on locally-cached config).
func pushManifest(cfg *config, lr *manifest.LoadResult) {
	if lr == nil || cfg.APIKey == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := manifest.Push(ctx, cfg.BaseURL, cfg.APIKey, lr)
	if err != nil {
		log.Printf("manifest upload failed: %v", err)
		return
	}
	if len(res.Errors) > 0 {
		log.Printf("manifest accepted but server flagged it invalid (status %d):\n  - %s",
			res.StatusCode, strings.Join(res.Errors, "\n  - "))
		return
	}
	log.Printf("manifest uploaded (status %d)", res.StatusCode)
}

// sendHeartbeat beacons inference.club so the provider stays "online" without
// the backend having to probe inward over the tailnet. Outbound POST with the
// same Bearer key used to register; the server stamps last_seen_at with its own
// receipt time. Best-effort — the caller logs and ignores failures.
func sendHeartbeat(c *config) error {
	if c.APIKey == "" {
		return fmt.Errorf("INFERENCE_CLUB_API_KEY not set")
	}
	c.mu.Lock()
	name := c.Name
	ip := c.TailnetIP
	c.mu.Unlock()
	payload := map[string]any{"name": name}
	if ip != "" {
		// Lets the backend dial the live node by IP, surviving tailnet hostname
		// drift on rejoin. Omitted in direct mode (no tailnet IP).
		payload["tailnet_addr"] = ip
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + "/api/inference/agent/heartbeat/"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("call heartbeat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat returned %d: %s", resp.StatusCode, b)
	}
	return nil
}

func register(c *config) (*registerResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"name":             c.Name,
		"tailnet_hostname": c.Hostname,
		"agent_port":       c.ListenPort,
	})
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + "/api/inference/agent/register/"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("register returned %d: %s", resp.StatusCode, b)
	}
	var rr registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}
	return &rr, nil
}
