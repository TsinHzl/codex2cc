package proxy

import (
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestIsClaudeCodeUserAgent(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
	}{
		{"claude-cli/2.0.0 (external, cli)", true},
		{"claude-code/1.0", true},
		{"Claude-CLI/2.0", true},
		{"claude-cli", true},
		{"Mozilla/5.0", false},
		{"", false},
		{"xclaude-cli/1.0", false},
	}
	for _, tc := range cases {
		if got := isClaudeCodeUserAgent(tc.ua); got != tc.want {
			t.Errorf("isClaudeCodeUserAgent(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

func TestFormatThinkingDurationLine(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{500 * time.Millisecond, ansiDim + "💭 Thought for 1s" + ansiReset},
		{38 * time.Second, ansiDim + "💭 Thought for 38s" + ansiReset},
		{65 * time.Second, ansiDim + "💭 Thought for 1m5s" + ansiReset},
		{125 * time.Second, ansiDim + "💭 Thought for 2m5s" + ansiReset},
	}
	for _, tc := range cases {
		if got := formatThinkingDurationLine(tc.elapsed); got != tc.want {
			t.Errorf("formatThinkingDurationLine(%v) = %q, want %q", tc.elapsed, got, tc.want)
		}
	}
}

func TestStripThinkingTextMarkers(t *testing.T) {
	header := ansiDim + "💭 Thinking" + ansiReset
	bare := header + "\n" + ansiDim + "first line" + ansiReset + "\n" + ansiDim + "second line" + ansiReset
	quoted := "> " + header + "\n> " + ansiDim + "first line" + ansiReset + "\n> " + ansiDim + "second line" + ansiReset

	// 裸格式整块剥离
	if got := stripThinkingTextMarkers("before\n" + bare + "\nafter"); got != "before\n\nafter" {
		t.Errorf("bare strip = %q", got)
	}
	// 引用格式整块剥离（不被裸正则截断）
	if got := stripThinkingTextMarkers("keep " + quoted + " tail"); got != "keep  tail" {
		t.Errorf("quoted strip = %q", got)
	}
	// 普通文本不受影响
	if got := stripThinkingTextMarkers("plain text"); got != "plain text" {
		t.Errorf("plain strip = %q", got)
	}
}

func TestStripThinkingTextHistory(t *testing.T) {
	header := ansiDim + "💭 Thinking" + ansiReset
	block := header + "\n" + ansiDim + "note" + ansiReset

	body := []byte(`{"model":"gpt-5","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"` + block + `real answer"},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + block + `"},{"type":"text","text":"kept"}]}` +
		`]}`)
	got := string(stripThinkingTextHistory(body))

	msgs := gjson.Get(got, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("messages len = %d, want 3", len(msgs))
	}
	if msgs[1].Get("content").String() != "real answer" {
		t.Errorf("string content = %q", msgs[1].Get("content").String())
	}
	blocks := msgs[2].Get("content").Array()
	if len(blocks) != 1 || blocks[0].Get("text").String() != "kept" {
		t.Errorf("array content = %s", msgs[2].Get("content").Raw)
	}
	// user 消息不受影响
	if msgs[0].Get("content").String() != "hi" {
		t.Errorf("user content = %q", msgs[0].Get("content").String())
	}
}

func TestStripThinkingTextHistoryNoop(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
	if got := stripThinkingTextHistory(body); string(got) != string(body) {
		t.Errorf("noop strip changed body: %s", got)
	}
}
