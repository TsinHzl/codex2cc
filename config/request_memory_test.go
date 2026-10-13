package config

import (
	"strings"
	"testing"
)

func TestRequestMemoryBudgetConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, maxBody, budget string
		want                  int64
		invalid               bool
	}{
		{"default", "48", "", 128 << 20, false},
		{"large_body_default", "256", "", 256 << 20, false},
		{"configured", "48", "512", 512 << 20, false},
		{"too_small", "48", "32", 0, true},
		{"zero", "48", "0", 0, true},
		{"negative", "48", "-1", 0, true},
		{"overflow", "48", "9223372036854775807", 0, true},
		{"invalid", "48", "wrong", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_DRIVER", "sqlite")
			t.Setenv("DATABASE_PATH", ":memory:")
			t.Setenv("CACHE_DRIVER", "memory")
			t.Setenv("CODEX_MAX_REQUEST_BODY_SIZE_MB", tc.maxBody)
			t.Setenv("CODEX_REQUEST_MEMORY_BUDGET_MB", tc.budget)
			cfg, err := Load("__not_exists__.env")
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "CODEX_REQUEST_MEMORY_BUDGET_MB") {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RequestMemoryBudgetBytes != tc.want {
				t.Fatalf("budget=%d want=%d", cfg.RequestMemoryBudgetBytes, tc.want)
			}
		})
	}
}
