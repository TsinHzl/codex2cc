package auth

import "testing"

func TestNormalizeCodexClientMetadataMode(t *testing.T) {
	tests := map[string]string{
		"":         CodexClientMetadataModeAuto,
		"AUTO":     CodexClientMetadataModeAuto,
		" always ": CodexClientMetadataModeAlways,
		"off":      CodexClientMetadataModeOff,
		"unknown":  CodexClientMetadataModeAuto,
	}
	for input, want := range tests {
		if got := NormalizeCodexClientMetadataMode(input); got != want {
			t.Errorf("NormalizeCodexClientMetadataMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsValidCodexClientMetadataMode(t *testing.T) {
	for _, value := range []string{"auto", "AUTO", "always", "off"} {
		if !IsValidCodexClientMetadataMode(value) {
			t.Errorf("IsValidCodexClientMetadataMode(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "sometimes"} {
		if IsValidCodexClientMetadataMode(value) {
			t.Fatalf("IsValidCodexClientMetadataMode(%q) accepted an invalid mode", value)
		}
	}
}

func TestNormalizeCodexPassthroughMode(t *testing.T) {
	tests := map[string]string{
		"":         CodexPassthroughModeOff,
		"off":      CodexPassthroughModeOff,
		"AUTO":     CodexPassthroughModeAuto,
		" always ": CodexPassthroughModeAlways,
		"unknown":  CodexPassthroughModeOff,
	}
	for input, want := range tests {
		if got := NormalizeCodexPassthroughMode(input); got != want {
			t.Errorf("NormalizeCodexPassthroughMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsValidCodexPassthroughMode(t *testing.T) {
	for _, value := range []string{"off", "auto", "always"} {
		if !IsValidCodexPassthroughMode(value) {
			t.Errorf("IsValidCodexPassthroughMode(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "sometimes"} {
		if IsValidCodexPassthroughMode(value) {
			t.Fatalf("IsValidCodexPassthroughMode(%q) accepted an invalid mode", value)
		}
	}
}
