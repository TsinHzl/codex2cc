package auth

import (
	"math"
	"time"
)

// 调度模式（remaining_quota / fill_first / round_robin 的 healthy 桶）的排序键是
// "账号已用配额百分比"，来源为 5h/7d 用量窗口（wham 探针、响应头）。
// 观测过期后视为未知（0），避免陈旧快照把已恢复的账号长期压在队尾。

// usagePercentForScheduling 返回调度排序用的已用配额百分比。
func (a *Account) usagePercentForScheduling() float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.usagePercentForSchedulingLocked(time.Now())
}

func (a *Account) usagePercentForSchedulingLocked(now time.Time) float64 {
	if a.UsagePercent7dValid {
		return a.UsagePercent7d
	}
	return 0
}

func clampSchedulingPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// schedulerUsageRefresher 由 Store 实现：账号侧的用量观测变化后让调度器重新评估该
// 账号的桶内位置。未挂到 Store 的账号（探测/临时账号）什么也不做。
type schedulerUsageRefresher interface {
	refreshSchedulerUsage(*Account)
}

func (s *Store) refreshSchedulerUsage(acc *Account) {
	s.fastSchedulerUpdate(acc)
}

// notifySchedulerUsageChanged 在账号锁之外调用。
func (a *Account) notifySchedulerUsageChanged(s schedulerUsageRefresher) {
	if a == nil || s == nil {
		return
	}
	s.refreshSchedulerUsage(a)
}
