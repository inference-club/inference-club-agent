package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PushResult is what the server returned. ``Errors`` is non-empty if the
// server's own validation found problems we missed locally — we surface
// them to the operator's logs but don't abort the agent (the manifest is
// already persisted server-side, just flagged invalid).
type PushResult struct {
	StatusCode int
	Errors     []string
}

// Push uploads the manifest to inference.club. The agent should already
// have a valid INFERENCE_CLUB_API_KEY (the same key it used to register).
//
// Body shape: ``{"raw_yaml": "...", "parsed": {...}}`` — server validates
// ``parsed`` independently and stores both.
func Push(ctx context.Context, baseURL, apiKey string, lr *LoadResult) (*PushResult, error) {
	if lr == nil || lr.Manifest == nil {
		return nil, fmt.Errorf("manifest is nil")
	}
	if apiKey == "" {
		return nil, fmt.Errorf(
			"INFERENCE_CLUB_API_KEY not set — required to upload manifest",
		)
	}

	// Strip per-service api_keys before they leave the agent. We send the
	// REDACTED re-serialization, not lr.Raw — otherwise a secret in the
	// operator's literal YAML would ride along in raw_yaml.
	rawYAML, parsed, err := lr.Manifest.RedactedForUpload()
	if err != nil {
		return nil, fmt.Errorf("encode manifest for upload: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"raw_yaml": string(rawYAML),
		"parsed":   parsed,
	})
	if err != nil {
		return nil, fmt.Errorf("encode upload body: %w", err)
	}

	endpoint := strings.TrimSuffix(baseURL, "/") + "/api/inference/agent/manifest/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call manifest upload: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// 200 = clean accept; 400 = persisted but server-side validation
	// failed (we surface the errors); anything else = unexpected, return
	// an error so the operator notices.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return nil, fmt.Errorf(
			"manifest upload returned %d: %s", resp.StatusCode, string(respBody),
		)
	}

	var parsedResp struct {
		Errors []string `json:"errors"`
	}
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &parsedResp)
	}
	return &PushResult{
		StatusCode: resp.StatusCode,
		Errors:     parsedResp.Errors,
	}, nil
}
