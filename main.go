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
//	AGENT_LISTEN_PORT        port inside the tailnet (default: 443)
//	AGENT_CONFIG_FILE        path to agent.yaml (default: /etc/inference-club-agent/agent.yaml)
//	TAILSCALE_LOGIN_SERVER   override for Headscale (default: empty → Tailscale's control plane)
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
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
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

	mf, mfErrs := loadManifest(cfg)
	if mfErrs != nil {
		log.Fatalf("manifest invalid — fix and restart:\n  - %s",
			strings.Join(mfErrs, "\n  - "))
	}
	// The manifest is the source of truth when present: agent.name and
	// agent.hostname/listen_port from YAML override env-var equivalents,
	// so the manifest the operator uploads describes the same identity
	// that registered with inference.club.
	applyManifestToConfig(cfg, mf)

	if err := ensureAuthKey(cfg); err != nil {
		log.Fatalf("registration failed: %v", err)
	}

	pushManifest(cfg, mf)

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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// SIGHUP → reload + revalidate + reupload the manifest. We do not
	// touch the running tsnet listener; manifest changes are
	// metadata-only from inference.club's perspective. The router
	// (Phase 1 of the agent ROADMAP) will pick this up to swap
	// backends; for PR 2 the only effect is the upload.
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			log.Print("SIGHUP received — reloading manifest")
			newMF, errs := loadManifest(cfg)
			if errs != nil {
				log.Printf("manifest reload failed (keeping previous):\n  - %s",
					strings.Join(errs, "\n  - "))
				continue
			}
			pushManifest(cfg, newMF)
		}
	}()

	log.Printf("starting tsnet hostname=%q state=%q", cfg.Hostname, cfg.StateDir)
	if _, err := srv.Up(ctx); err != nil {
		log.Fatalf("tsnet up: %v", err)
	}

	target, err := url.Parse(cfg.LocalLLMURL)
	if err != nil {
		log.Fatalf("invalid LOCAL_LLM_URL: %v", err)
	}
	proxy := newOpenAIProxy(target)

	listener, err := srv.Listen("tcp", fmt.Sprintf(":%d", cfg.ListenPort))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found — try /v1/models", http.StatusNotFound)
	})

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	log.Printf("serving on tailnet port %d → %s", cfg.ListenPort, cfg.LocalLLMURL)
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

// newOpenAIProxy returns a reverse proxy that rewrites every request to
// hit `target`. SSE streaming completions flush as data arrives.
func newOpenAIProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Capture the incoming path BEFORE defaultDirector clobbers it via
		// singleJoiningSlash(target.Path, req.URL.Path). Without this, a
		// LOCAL_LLM_URL ending in /v1 produces /v1/v1/models on the upstream.
		origPath := req.URL.Path
		defaultDirector(req)
		req.URL.Path = strings.TrimSuffix(target.Path, "/") + strings.TrimPrefix(origPath, "/v1")
		req.Host = target.Host
		req.Header.Set("X-Forwarded-Host", target.Host)
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
	}
	return proxy
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

	mu sync.Mutex // guards Name/Hostname/ListenPort under SIGHUP reload
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
