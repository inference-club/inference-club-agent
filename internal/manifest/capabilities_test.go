package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func manifestWithCaps() *Manifest {
	return &Manifest{
		SchemaVersion: 1,
		Agent:         Agent{Name: "club-host"},
		Hosts: []Host{{
			ID: "rig-01",
			Services: []Service{{
				Name:   "vllm",
				Engine: "vllm",
				Type:   "llm",
				URL:    "http://192.168.1.10:8000/v1",
				Models: []Model{{
					ID:               "qwen3-30b-a3b",
					Hf:               "Qwen/Qwen3-30B-A3B",
					Name:             "Qwen3 30B A3B",
					InputModalities:  []string{"text", "image"},
					OutputModalities: []string{"text"},
					Features:         []string{"reasoning", "tools"},
					ContextLength:    32768,
					Quantization:     "fp8",
				}},
			}},
		}},
	}
}

// Declared capabilities must survive the upload round-trip in BOTH halves
// (raw_yaml and parsed JSON) so the server can persist them.
func TestCapabilities_SurviveUpload(t *testing.T) {
	rawYAML, parsed, err := manifestWithCaps().RedactedForUpload()
	if err != nil {
		t.Fatalf("RedactedForUpload: %v", err)
	}
	for _, want := range []string{"input_modalities", "features", "context_length", "quantization", "Qwen3 30B A3B"} {
		if !strings.Contains(string(rawYAML), want) {
			t.Errorf("raw_yaml missing %q:\n%s", want, rawYAML)
		}
	}
	pj, _ := json.Marshal(parsed)
	for _, want := range []string{"input_modalities", "context_length", "quantization"} {
		if !strings.Contains(string(pj), want) {
			t.Errorf("parsed JSON missing %q: %s", want, pj)
		}
	}
}

func TestCapabilities_ValidateAcceptsDeclared(t *testing.T) {
	if errs := Validate(manifestWithCaps()); len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestCapabilities_ValidateRejectsNegativeContext(t *testing.T) {
	m := manifestWithCaps()
	m.Hosts[0].Services[0].Models[0].ContextLength = -1
	errs := Validate(m)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "context_length") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a context_length error, got: %v", errs)
	}
}
