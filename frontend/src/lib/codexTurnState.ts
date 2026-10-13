// X-Codex-Turn-State 强制注入的时效计算。上游实测该值在设置后约 1 小时失效,
// 这里只做纯函数:解析后端记录的 set_at、算剩余毫秒、格式化 mm:ss。UI 层负责每秒重算。

/** 上游实测的 turn-state 时效(毫秒)。 */
export const CODEX_TURN_STATE_TTL_MS = 60 * 60 * 1000

/** 剩余不足该值时 UI 切换为警示色。 */
export const CODEX_TURN_STATE_WARN_MS = 10 * 60 * 1000

/** 解析后端返回的 RFC3339 set_at;空/不可解析返回 null(视为时效未知)。 */
export function parseCodexTurnStateSetAt(value?: string | null): number | null {
  if (typeof value !== 'string') return null
  const trimmed = value.trim()
  if (!trimmed) return null
  const ms = Date.parse(trimmed)
  return Number.isFinite(ms) ? ms : null
}

export type CodexTurnStateTtl =
  | { kind: 'unknown' }
  | { kind: 'expired'; remainingMs: 0 }
  | { kind: 'active'; remainingMs: number; ratio: number; warning: boolean }

/** 按 1 小时 TTL 计算剩余状态。ratio 为剩余占比 [0,1],供进度条使用。 */
export function computeCodexTurnStateTtl(
  setAt?: string | null,
  now: number = Date.now(),
  ttlMs: number = CODEX_TURN_STATE_TTL_MS,
): CodexTurnStateTtl {
  const setAtMs = parseCodexTurnStateSetAt(setAt)
  if (setAtMs === null) return { kind: 'unknown' }
  const remainingMs = setAtMs + ttlMs - now
  if (remainingMs <= 0) return { kind: 'expired', remainingMs: 0 }
  const clamped = Math.min(remainingMs, ttlMs)
  return {
    kind: 'active',
    remainingMs: clamped,
    ratio: clamped / ttlMs,
    warning: clamped < CODEX_TURN_STATE_WARN_MS,
  }
}

/** 毫秒 → "mm:ss"(向上取整到秒,避免刚保存时显示 59:59)。超过 99 分钟不截断。 */
export function formatCodexTurnStateCountdown(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.ceil(remainingMs / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
}
