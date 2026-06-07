package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func manifestWithKey() *Manifest {
	return &Manifest{
		SchemaVersion: 1,
		Agent:         Agent{Name: "club-host"},
		Hosts: []Host{{
			ID: "dgx-spark",
			Services: []Service{{
				Name:   "dgx-spark-lmstudio",
				Engine: "lmstudio",
				URL:    "http://192.168.6.12:1234/v1",
				APIKey: "sk-lm-SUPERSECRET:xyz",
				Models: []Model{{ID: "google/gemma-4-12b", Hf: "google/gemma-4-12B"}},
			}},
		}},
	}
}

// The api_key must never appear in EITHER half of the upload payload
// (raw_yaml or parsed) — this is the whole privacy guarantee.
func TestRedactedForUpload_StripsAPIKey(t *testing.T) {
	m := manifestWithKey()
	rawYAML, parsed, err := m.RedactedForUpload()
	if err != nil {
		t.Fatalf("RedactedForUpload: %v", err)
	}

	if strings.Contains(string(rawYAML), "SUPERSECRET") {
		t.Errorf("raw_yaml leaked the api_key:\n%s", rawYAML)
	}
	if strings.Contains(strings.ToLower(string(rawYAML)), "api_key") {
		t.Errorf("raw_yaml still contains an api_key field:\n%s", rawYAML)
	}

	pj, _ := json.Marshal(parsed)
	if strings.Contains(string(pj), "SUPERSECRET") {
		t.Errorf("parsed leaked the api_key: %s", pj)
	}

	// The model id must survive redaction (we only strip the secret).
	if !strings.Contains(string(rawYAML), "google/gemma-4-12b") {
		t.Errorf("redaction dropped the model id:\n%s", rawYAML)
	}

	// The original in-memory manifest must be untouched (redaction copies).
	if m.Hosts[0].Services[0].APIKey == "" {
		t.Error("Redacted mutated the original manifest's api_key")
	}
}

// AsJSONMap (used for the `parsed` half) must also exclude the key on its own,
// via the json:"-" tag, independent of redaction.
func TestAsJSONMap_ExcludesAPIKey(t *testing.T) {
	m := manifestWithKey()
	jm, err := m.AsJSONMap()
	if err != nil {
		t.Fatalf("AsJSONMap: %v", err)
	}
	b, _ := json.Marshal(jm)
	if strings.Contains(string(b), "SUPERSECRET") || strings.Contains(string(b), "api_key") {
		t.Errorf("AsJSONMap leaked api_key: %s", b)
	}
}
