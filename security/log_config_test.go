package security

import (
	"path/filepath"
	"testing"
)

func TestFileLogsDisabled(t *testing.T) {
	t.Setenv("LOG_DISABLED", "true")
	if !FileLogsDisabled() {
		t.Fatal("FileLogsDisabled() = false, want true")
	}
}

func TestSecurityLogDirUsesEnv(t *testing.T) {
	t.Setenv("LOG_DIR", "/tmp/codex2cc-logs")
	if got, want := securityLogDir(), filepath.Join("/tmp/codex2cc-logs", "security"); got != want {
		t.Fatalf("securityLogDir() = %q, want %q", got, want)
	}

	t.Setenv("SECURITY_LOG_DIR", "/tmp/codex2cc-security")
	if got, want := securityLogDir(), "/tmp/codex2cc-security"; got != want {
		t.Fatalf("securityLogDir() = %q, want %q", got, want)
	}
}
