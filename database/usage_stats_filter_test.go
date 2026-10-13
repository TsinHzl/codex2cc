package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 用量页选中某个账号后,顶部区间卡片曾经仍按全局口径显示(接口没有账号维度)。
// 现在区间字段(today_*/rpm/tpm/错误率/分项)跟随维度筛选,累计字段保持全局。
func TestGetUsageStatsFilteredNarrowsRangeFieldsKeepsTotals(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "stats-filter.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	logs := []*UsageLogInput{
		{AccountID: 1, APIKeyID: 10, Endpoint: "/v1/responses", Model: "gpt-5.4", StatusCode: 200, TotalTokens: 100},
		{AccountID: 1, APIKeyID: 10, Endpoint: "/v1/responses", Model: "gpt-5.4", StatusCode: 500, TotalTokens: 50},
		{AccountID: 2, APIKeyID: 11, Endpoint: "/v1/responses", Model: "gpt-5.6-sol", StatusCode: 200, TotalTokens: 1000},
		{AccountID: 2, APIKeyID: 11, Endpoint: "/v1/responses", Model: "gpt-5.6-sol", StatusCode: 200, TotalTokens: 1000},
		{AccountID: 2, APIKeyID: 11, Endpoint: "/v1/responses", Model: "gpt-5.6-sol", StatusCode: 200, TotalTokens: 1000},
	}
	for _, input := range logs {
		if err := db.InsertUsageLog(ctx, input); err != nil {
			t.Fatalf("InsertUsageLog: %v", err)
		}
	}
	db.flushLogs()

	now := time.Now()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)

	global, err := db.GetUsageStats(ctx, start, end, "")
	if err != nil {
		t.Fatalf("GetUsageStats: %v", err)
	}
	if global.TodayRequests != 5 || global.TotalRequests != 5 {
		t.Fatalf("global today=%d total=%d, want 5/5", global.TodayRequests, global.TotalRequests)
	}

	accountID := int64(1)
	byAccount, err := db.GetUsageStatsFiltered(ctx, start, end, "", UsageLogFilter{AccountID: &accountID}, true)
	if err != nil {
		t.Fatalf("GetUsageStatsFiltered(account): %v", err)
	}
	if byAccount.TodayRequests != 2 || byAccount.TodayTokens != 150 {
		t.Fatalf("account today=%d tokens=%d, want 2/150", byAccount.TodayRequests, byAccount.TodayTokens)
	}
	if byAccount.ErrorRate < 49.9 || byAccount.ErrorRate > 50.1 {
		t.Fatalf("account error rate = %.2f, want 50", byAccount.ErrorRate)
	}
	if byAccount.TotalRequests != 5 || byAccount.TotalTokens != global.TotalTokens {
		t.Fatalf("account totals = %d/%d, want global 5/%d (累计保持全局)", byAccount.TotalRequests, byAccount.TotalTokens, global.TotalTokens)
	}
	if len(byAccount.ModelStats) != 1 || byAccount.ModelStats[0].Model != "gpt-5.4" {
		t.Fatalf("account model stats = %+v, want only gpt-5.4", byAccount.ModelStats)
	}
	if byAccount.FeatureStats.ErrorRequests != 1 {
		t.Fatalf("account feature error requests = %d, want 1", byAccount.FeatureStats.ErrorRequests)
	}
	if len(byAccount.APIKeyStats) != 1 || byAccount.APIKeyStats[0].APIKeyID != 10 {
		t.Fatalf("account api key stats = %+v, want only key 10", byAccount.APIKeyStats)
	}

	// 搜索词命中模型名也能收窄。
	byQuery, err := db.GetUsageStatsFiltered(ctx, start, end, "", UsageLogFilter{Query: "5.6-sol"}, false)
	if err != nil {
		t.Fatalf("GetUsageStatsFiltered(query): %v", err)
	}
	if byQuery.TodayRequests != 3 || byQuery.TodayTokens != 3000 {
		t.Fatalf("query today=%d tokens=%d, want 3/3000", byQuery.TodayRequests, byQuery.TodayTokens)
	}

	// 状态类条件不是维度:带 ErrorOnly 的筛选与不带等价。
	if (UsageLogFilter{ErrorOnly: true, StatusFamily: "5xx"}).HasDimensionFilter() {
		t.Fatal("status-only filter must not count as dimension filter")
	}
	statusOnly, err := db.GetUsageStatsFiltered(ctx, start, end, "", UsageLogFilter{ErrorOnly: true, StatusFamily: "5xx"}, false)
	if err != nil {
		t.Fatalf("GetUsageStatsFiltered(status): %v", err)
	}
	if statusOnly.TodayRequests != 5 {
		t.Fatalf("status-only today=%d, want 5", statusOnly.TodayRequests)
	}
	if (UsageLogFilter{AccountID: &accountID}).DimensionKey() == (UsageLogFilter{Query: "x"}).DimensionKey() {
		t.Fatal("different dimension filters must have different keys")
	}
}
