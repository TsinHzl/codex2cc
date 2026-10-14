package proxy

// thinking → dim text 渲染（Claude Code 终端展示「💭 Thinking」灰度文案）。
// 逐字节对齐 cc-proxy src/thinking-text.js 的渲染约定：逐行独立 dim 包裹、
// newline 引导下一行（块尾不带 '\n'）、空行过滤、stop 时补 duration 行。
// 仅对 Claude Code UA 生效；非流式响应不做转换。

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// thinkingTextHeaderLine 逐行独立 dim 并在每个换行前 reset，样式不跨行；
// 无 `> ` 前缀：纯 dim 行可避免 Claude Code 为引用行渲染的竖条。
const thinkingTextHeaderLine = ansiDim + "💭 Thinking" + ansiReset

// claudeCodeUserAgentRE 同 cc-proxy src/server/routes-messages.js 的 UA 判定。
var claudeCodeUserAgentRE = regexp.MustCompile(`(?i)^(?:claude-cli|claude-code)(?:/|\s|$)`)

func isClaudeCodeUserAgent(userAgent string) bool {
	return claudeCodeUserAgentRE.MatchString(userAgent)
}

// 历史剥离正则：引用格式（`> ` 前缀）必须先于裸格式剥离，否则裸正则会
// 截断引用块内部的 dim header。本仓库未产出过 legacy 标记格式，不移植该正则。
var (
	quotedThinkingTextBlockRE = regexp.MustCompile(
		regexp.QuoteMeta("> "+ansiDim+"💭 Thinking"+ansiReset) +
			`(?:\n> \x1b\[2m[^\n]*?\x1b\[0m)*`)
	thinkingTextBlockRE = regexp.MustCompile(
		regexp.QuoteMeta(thinkingTextHeaderLine) +
			`(?:\n\x1b\[2m[^\n]*?\x1b\[0m)*`)
)

// stripThinkingTextMarkers 按顺序剥离引用格式与裸格式的 thinking 文案块。
func stripThinkingTextMarkers(text string) string {
	text = quotedThinkingTextBlockRE.ReplaceAllString(text, "")
	return thinkingTextBlockRE.ReplaceAllString(text, "")
}

// formatThinkingDurationLine 生成 stop 时的时长行，如 `💭 Thought for 12s`，
// ≥60s 显示 `XmYs`；整秒、1s 下限，对齐 cc-proxy formatDurationLine。
func formatThinkingDurationLine(elapsed time.Duration) string {
	total := int(math.Round(elapsed.Seconds()))
	if total < 1 {
		total = 1
	}
	label := fmt.Sprintf("%ds", total)
	if total >= 60 {
		label = fmt.Sprintf("%dm%ds", total/60, total%60)
	}
	return ansiDim + "💭 Thought for " + label + ansiReset
}

// stripThinkingTextHistory 从 /v1/messages 请求体中剥离 assistant 历史里
// 由本代理渲染出的 thinking 文案块（string content 与 content 数组中的
// type=text 块），内容剥空的消息整条丢弃。sjson 原地替换以保留原始键序；
// 倒序遍历使删除消息后后续索引路径仍然有效。
func stripThinkingTextHistory(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	all := messages.Array()
	var setErr error
	for i := len(all) - 1; i >= 0; i-- {
		msg := all[i]
		if msg.Get("role").String() != "assistant" {
			continue
		}
		path := "messages." + strconv.Itoa(i) + ".content"
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			stripped := stripThinkingTextMarkers(content.String())
			if stripped == content.String() {
				continue
			}
			if stripped == "" {
				// 剥空后整条消息丢弃
				var updated []byte
				updated, setErr = sjson.DeleteBytes(body, "messages."+strconv.Itoa(i))
				if setErr != nil {
					return body
				}
				body = updated
				continue
			}
			var updated []byte
			updated, setErr = sjson.SetBytes(body, path, stripped)
			if setErr != nil {
				return body
			}
			body = updated
		case content.IsArray():
			var rebuilt []json.RawMessage
			modified := false
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() != "text" {
					rebuilt = append(rebuilt, json.RawMessage(block.Raw))
					return true
				}
				text := block.Get("text").String()
				stripped := stripThinkingTextMarkers(text)
				if stripped == text {
					rebuilt = append(rebuilt, json.RawMessage(block.Raw))
					return true
				}
				if stripped == "" {
					// 剥空后整块丢弃
					modified = true
					return true
				}
				updated, err := sjson.SetBytes([]byte(block.Raw), "text", stripped)
				if err != nil {
					rebuilt = append(rebuilt, json.RawMessage(block.Raw))
					return true
				}
				rebuilt = append(rebuilt, json.RawMessage(updated))
				modified = true
				return true
			})
			if !modified {
				continue
			}
			// 全部块被剥空 → content 为空，与 string 分支一致整条丢弃
			if len(rebuilt) == 0 {
				var updated []byte
				updated, setErr = sjson.DeleteBytes(body, "messages."+strconv.Itoa(i))
				if setErr != nil {
					return body
				}
				body = updated
				continue
			}
			encoded, err := json.Marshal(rebuilt)
			if err != nil {
				return body
			}
			var updated []byte
			updated, setErr = sjson.SetRawBytes(body, path, encoded)
			if setErr != nil {
				return body
			}
			body = updated
		}
	}
	return body
}
