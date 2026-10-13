package auth

import "testing"



func TestUsagePercentForSchedulingCodexPathUnchanged(t *testing.T) {
	acc := &Account{DBID: 1, AccessToken: "token", UsagePercent7d: 42, UsagePercent7dValid: true}
	if got := acc.usagePercentForScheduling(); got != 42 {
		t.Fatalf("codex 7d usage: got %v, want 42", got)
	}
	acc.UsagePercent7dValid = false
	if got := acc.usagePercentForScheduling(); got != 0 {
		t.Fatalf("codex without valid 7d usage: got %v, want 0", got)
	}
}






type recordingSchedulerRefresher struct {
	scheduler *FastScheduler
	calls     int
}

func (r *recordingSchedulerRefresher) refreshSchedulerUsage(acc *Account) {
	r.calls++
	r.scheduler.Update(acc)
}
