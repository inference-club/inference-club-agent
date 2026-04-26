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
//	LOCAL_LLM_URL            e.g. http://host.docker.internal:1234/v1
//	AGENT_NAME               friendly name shown in the inference.club UI
//	AGENT_HOSTNAME           tailnet hostname (default: club-host-<rand>)
//	AGENT_STATE_DIR          where to cache tsnet state + authkey (default: /var/lib/club-host)
//	AGENT_LISTEN_PORT        port inside the tailnet (default: 443)
//	TAILSCALE_LOGIN_SERVER   override for Headscale (default: empty → Tailscale's control plane)
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
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

func main() {
	cfg := loadConfig()

	if err := ensureAuthKey(cfg); err != nil {
		log.Fatalf("registration failed: %v", err)
	}

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
		defaultDirector(req)
		// httputil doesn't preserve target.Path; route /v1/* to {target.Path}/*.
		req.URL.Path = strings.TrimSuffix(target.Path, "/") + strings.TrimPrefix(req.URL.Path, "/v1")
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
	if data, err := os.ReadFile(keyPath); err == nil && len(bytes.TrimSpace(data)) > 0 {
		c.AuthKey = string(bytes.TrimSpace(data))
		log.Print("loaded cached tailscale authkey")
		return nil
	}

	if c.APIKey == "" {
		return errors.New("no cached authkey and INFERENCE_CLUB_API_KEY not set — generate one at https://inference.club/dashboard and pass it via env")
	}

	resp, err := register(c)
	if err != nil {
		return err
	}
	if resp.TailscaleAuthkey == "" {
		return errors.New("register endpoint returned an empty tailscale_authkey — central server's Tailscale OAuth client may not be configured")
	}
	if err := os.WriteFile(keyPath, []byte(resp.TailscaleAuthkey), 0o600); err != nil {
		return fmt.Errorf("write authkey: %w", err)
	}
	c.AuthKey = resp.TailscaleAuthkey
	if resp.TailscaleLoginServer != "" {
		c.LoginServer = resp.TailscaleLoginServer
	}
	if resp.TailnetHostname != "" {
		// Server is allowed to dictate the tailnet hostname so it lines up
		// with the Provider record on its end.
		c.Hostname = resp.TailnetHostname
	}
	log.Printf("registered as provider_id=%d tailnet_hostname=%s", resp.ProviderID, c.Hostname)
	return nil
}

type registerResponse struct {
	ProviderID           int    `json:"provider_id"`
	TailscaleAuthkey     string `json:"tailscale_authkey"`
	TailscaleLoginServer string `json:"tailscale_login_server"`
	TailnetHostname      string `json:"tailnet_hostname"`
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
