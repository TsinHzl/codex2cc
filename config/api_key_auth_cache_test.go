package config

import "testing"

func TestAPIKeyAuthCacheConfiguration(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "sqlite")
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("CACHE_DRIVER", "memory")
	for _, tc := range []struct {
		value   string
		enabled bool
		bad     bool
	}{{"", true, false}, {"true", true, false}, {"false", false, false}, {"invalid", false, true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("CODEX_API_KEY_AUTH_CACHE_ENABLED", tc.value)
			cfg, err := Load("__not_exists__.env")
			if tc.bad {
				if err == nil {
					t.Fatal("invalid cache flag accepted")
				}
				return
			}
			if err != nil || cfg.APIKeyAuthCacheEnabled != tc.enabled {
				t.Fatalf("cache flag: %+v %v", cfg, err)
			}
		})
	}
}
