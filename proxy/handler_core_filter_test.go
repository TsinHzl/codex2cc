package proxy

import (
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
)

// 表驱动验证 claudeChannelAccountFilter 的 Codex OAuth + relay 双分支准入：
// relay 按 openai_responses 白名单/映射判定，Codex OAuth 按 Codex 白名单判定，
// 两类账号共用模型级冷却门。
func TestClaudeChannelAccountFilter(t *testing.T) {
	relay := &auth.Account{
		UpstreamType: "openai_responses",
		BaseURL:      "https://relay.example.com",
		APIKey:       "sk-relay",
		Models:       []string{"gpt-5.5", "gpt-5.6-luna"},
	}
	codex := &auth.Account{
		AccessToken: "at",
		PlanType:    "plus",
		Models:      []string{"gpt-5.5"},
	}
	codexNoWhitelist := &auth.Account{
		AccessToken: "at",
		PlanType:    "plus",
	}
	codexCooldown := &auth.Account{
		AccessToken: "at",
		PlanType:    "plus",
	}
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 4, TestConcurrency: 1, TestModel: "gpt-5.5"})
	store.AddAccount(codexCooldown)
	store.MarkModelCooldown(codexCooldown, "gpt-5.5", 30*time.Minute, "rate_limited")

	relayCooldown := &auth.Account{
		UpstreamType: "openai_responses",
		BaseURL:      "https://relay.example.com",
		APIKey:       "sk-relay",
		Models:       []string{"gpt-5.5"},
	}
	store.AddAccount(relayCooldown)
	store.MarkModelCooldown(relayCooldown, "gpt-5.5", 30*time.Minute, "rate_limited")

	// relay 带模型映射：claude-sonnet-4-5 → gpt-5.5，应按映射后模型判定白名单与冷却。
	relayWithMapping := &auth.Account{
		UpstreamType: "openai_responses",
		BaseURL:      "https://relay.example.com",
		APIKey:       "sk-relay",
		Models:       []string{"gpt-5.5"},
		ModelMapping: `{"claude-sonnet-4-5":"gpt-5.5"}`,
	}

	cases := []struct {
		name    string
		model   string
		account *auth.Account
		want    bool
	}{
		{"relay 白名单命中", "gpt-5.5", relay, true},
		{"relay 白名单未命中", "gpt-5.4", relay, false},
		{"relay 模型冷却排除", "gpt-5.5", relayCooldown, false},
		{"relay 映射命中白名单", "claude-sonnet-4-5", relayWithMapping, true},
		{"relay 空模型拒绝", "", relay, false},
		{"codex 空模型放行（既有语义）", "", codex, true},
		{"codex 空白名单放行", "gpt-5.4", codexNoWhitelist, true},
		{"codex 白名单命中", "gpt-5.5", codex, true},
		{"codex 白名单未命中", "gpt-5.6-luna", codex, false},
		{"codex 模型冷却排除", "gpt-5.5", codexCooldown, false},
		{"nil 账号拒绝", "gpt-5.5", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := claudeChannelAccountFilter(tc.model)
			if got := filter(tc.account); got != tc.want {
				t.Fatalf("claudeChannelAccountFilter(%q)(account) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
