package auth

import (
	"strings"
	"time"
)

// AccountTimezoneCredentialKey 是账号绑定时区在 credentials 中的存储键
// 早已用它做身份一致性标签；Codex 官方账号复用同一键，含义是"该账号对上游呈现的
// 时区"，空 = 不绑定、下游客户端的时区原样透传。
const AccountTimezoneCredentialKey = "timezone"

// NormalizeAccountTimezone 归一化账号绑定时区：空串、"Local" 与无法加载的 IANA 名字
// 一律返回空（= 不绑定）。宁可静默回落也不要让一个坏值把整个账号的请求打断。
func NormalizeAccountTimezone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "Local") {
		return ""
	}
	if _, err := time.LoadLocation(value); err != nil {
		return ""
	}
	return value
}

// EffectiveCodexTimezone 返回 Codex 官方出站路径生效的绑定时区。
// 中转型账号（OpenAI Responses）不走 Codex 官方出站，恒为空。
func (a *Account) EffectiveCodexTimezone() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isOpenAIResponsesAPILocked() {
		return ""
	}
	return strings.TrimSpace(a.Timezone)
}

// ApplyAccountTimezone 把管理端改动的绑定时区同步到运行时账号，
// 避免等到下一次全量重载才生效。
func (s *Store) ApplyAccountTimezone(dbID int64, timezone string) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	acc.Timezone = NormalizeAccountTimezone(timezone)
	acc.mu.Unlock()
	return true
}
