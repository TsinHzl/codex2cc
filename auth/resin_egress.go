package auth

import "sync/atomic"

// resinEgressEnabled 记录 Resin 反代是否承担 Codex 渠道出站。配置本体在 proxy 包
// (auth 不能反向依赖 proxy),由 proxy.SetResinConfig 同步写入。调度器的代理池
// fail-closed 过滤据此放行 Codex 账号:Resin 自带出口 IP,"池空/绑定的代理已禁用"
// 不再意味着账号会脏 IP 直连(issue #679)。
var resinEgressEnabled atomic.Bool

// SetResinEgressEnabled 由 proxy 包在 Resin 启用/禁用时调用。
func SetResinEgressEnabled(enabled bool) {
	resinEgressEnabled.Store(enabled)
}

// ResinEgressEnabled 报告 Resin 是否正在承担 Codex 渠道出站。
func ResinEgressEnabled() bool {
	return resinEgressEnabled.Load()
}
