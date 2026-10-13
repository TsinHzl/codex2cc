package config

import (
	"strings"
	"testing"
)

func TestLoadSchedulerWaitLimits(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("CACHE_DRIVER", "memory")
	for _, tc := range []struct {
		total, perKey         string
		wantTotal, wantPerKey int
		bad                   string
	}{
		{"", "", 0, 0, ""},
		{"1024", "64", 1024, 64, ""},
		{"0", "64", 0, 0, "CODEX_SCHEDULER_MAX_WAITERS"},
		{"1024", "-1", 0, 0, "CODEX_SCHEDULER_MAX_WAITERS_PER_KEY"},
		{"invalid", "64", 0, 0, "CODEX_SCHEDULER_MAX_WAITERS"},
	} {
		t.Run(tc.total+"/"+tc.perKey, func(t *testing.T) {
			t.Setenv("CODEX_SCHEDULER_MAX_WAITERS", tc.total)
			t.Setenv("CODEX_SCHEDULER_MAX_WAITERS_PER_KEY", tc.perKey)
			cfg, err := Load("__not_exists__.env")
			if tc.bad != "" {
				if err == nil || !strings.Contains(err.Error(), tc.bad) {
					t.Fatalf("invalid limits error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SchedulerMaxWaiters != tc.wantTotal || cfg.SchedulerMaxWaitersPerKey != tc.wantPerKey {
				t.Fatalf("limits = %d/%d", cfg.SchedulerMaxWaiters, cfg.SchedulerMaxWaitersPerKey)
			}
		})
	}
}
