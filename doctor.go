package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
)

// runDoctor loads + validates the manifest and probes each service URL.
// Returns a process exit code (0 on success, 1 on any failure).
//
// Designed to be runnable inside the running container:
//
//	docker exec club-host host-agent doctor
//
// so the operator can debug "is my YAML good and can the agent see my
// LLM servers?" without leaving the deploy.
func runDoctor() int {
	// doctor prints its own formatted report via fmt; silence the standard
	// logger so loadManifest's "loaded manifest from ..." line (useful at
	// startup/SIGHUP) doesn't interleave into the report.
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	cfg := loadConfig()

	fmt.Printf("config file: %s\n", cfg.ConfigFile)
	if _, err := os.Stat(cfg.ConfigFile); os.IsNotExist(err) {
		fmt.Println("  (no manifest present — would synthesize from env vars)")
	}

	lr, errs := loadManifest(cfg)
	if errs != nil {
		fmt.Println("✗ manifest validation failed:")
		for _, e := range errs {
			fmt.Printf("  - %s\n", e)
		}
		return 1
	}
	m := lr.Manifest
	fmt.Printf("✓ manifest valid (schema_version=%d, agent.name=%q, hosts=%d)\n",
		m.SchemaVersion, m.Agent.Name, len(m.Hosts))

	probeFailed := false
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, h := range m.Hosts {
		fmt.Printf("\nhost %s", h.ID)
		if h.Address != "" {
			fmt.Printf(" (%s)", h.Address)
		}
		if h.GPU != nil && h.GPU.Model != "" {
			fmt.Printf(" — %s", h.GPU.Model)
		}
		fmt.Println()

		for _, s := range h.Services {
			ok, msg := probeService(probeCtx, s)
			marker := "✓"
			if !ok {
				marker = "✗"
				probeFailed = true
			}
			fmt.Printf("  %s %s [%s] %s — %s\n", marker, s.Name, s.Engine, s.URL, msg)
		}
	}

	if probeFailed {
		fmt.Println("\nat least one service was not reachable. Common causes:")
		fmt.Println("  - on Linux, missing --add-host=host.docker.internal:host-gateway")
		fmt.Println("  - LAN IP changed (router DHCP) — re-check the address")
		fmt.Println("  - the LLM server isn't running")
		return 1
	}
	fmt.Println("\nall services reachable.")
	return 0
}

// probeService GETs <url>/models and reports whether the response looks
// like an OpenAI-compatible /v1/models surface.
func probeService(ctx context.Context, s manifest.Service) (bool, string) {
	endpoint := strings.TrimSuffix(s.URL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Sprintf("build request: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("unreachable (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Sprintf("HTTP %d on GET %s", resp.StatusCode, endpoint)
	}
	return true, fmt.Sprintf("HTTP %d", resp.StatusCode)
}
