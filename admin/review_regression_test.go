package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewShortDiagnosticSecretsPreserveIDs(t *testing.T) {
	got := sanitizeCodexTestText(`{"response_id":"resp_0123","usage":1234,"api_key":"1","echo":"1 1"}`, []string{"1"})
	var parsed map[string]any
	if json.Unmarshal([]byte(got), &parsed) != nil || parsed["response_id"] != "resp_0123" || parsed["usage"] != float64(1234) {
		t.Fatalf("short placeholder corrupted diagnostics: %s", got)
	}
	if strings.Contains(got, `"1"`) || strings.Contains(got, `1 1`) {
		t.Fatal("standalone credential was exposed")
	}
}
