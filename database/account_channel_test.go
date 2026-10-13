package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAPIKeyLimitsResolveUpstreamChannel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "auto empty", in: "", want: UpstreamChannelAuto},
		{name: "codex", in: " CODEX ", want: UpstreamChannelCodex},
		{name: "relay", in: " Relay ", want: UpstreamChannelRelay},
		{name: "unknown", in: "other", want: UpstreamChannelAuto},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (APIKeyLimits{UpstreamChannel: tt.in}).ResolveUpstreamChannel(); got != tt.want {
				t.Fatalf("ResolveUpstreamChannel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeAccountGroupChannel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "codex", want: AccountGroupChannelCodex},
		{in: " RELAY ", want: AccountGroupChannelRelay},
		{in: "Claude", want: AccountGroupChannelCodex},
		{in: "", want: AccountGroupChannelCodex},
		{in: "other", want: AccountGroupChannelCodex},
	}
	for _, tt := range tests {
		if got := NormalizeAccountGroupChannel(tt.in); got != tt.want {
			t.Errorf("NormalizeAccountGroupChannel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSQLiteListAccountListProjectionByChannel(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "account-list-projection-channel.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	codexID, err := db.InsertAccount(ctx, "codex", "codex-refresh", "")
	if err != nil {
		t.Fatalf("insert codex account: %v", err)
	}
	relayID, err := db.InsertAccountWithUpstream(ctx, "relay", "relay", "api_key", map[string]interface{}{
		"upstream_type": "relay",
		"api_key":       "relay-secret",
	}, "")
	if err != nil {
		t.Fatalf("insert relay account: %v", err)
	}

	tests := []struct {
		channel string
		wantID  int64
	}{
		{channel: UpstreamChannelCodex, wantID: codexID},
		{channel: UpstreamChannelRelay, wantID: relayID},
	}
	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			rows, err := db.ListAccountListProjection(ctx, tt.channel)
			if err != nil {
				t.Fatalf("ListAccountListProjection(%q) error: %v", tt.channel, err)
			}
			if len(rows) != 1 || rows[0].ID != tt.wantID {
				t.Fatalf("ListAccountListProjection(%q) = %+v, want id %d", tt.channel, rows, tt.wantID)
			}
		})
	}
}

func TestReplaceAccountCredentialsCASUpdatesCanonicalFamily(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "relay-family-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	id, err := db.InsertAccountWithUpstream(ctx, "relay", "relay", "oauth", map[string]interface{}{
		"upstream_type":        "relay",
		"refresh_token":        "old-refresh",
		"credential_family_id": "ag_old",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	newGeneration, applied, err := db.ReplaceAccountCredentialsCAS(ctx, id, 1, "ag_new", map[string]any{
		"refresh_token": "new-refresh",
	})
	if err != nil || !applied || newGeneration != 2 {
		t.Fatalf("ReplaceAccountCredentialsCAS() = generation %d applied %v err %v", newGeneration, applied, err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.CredentialGeneration != 2 || row.CredentialFamilyID != "ag_new" || row.GetCredential("credential_family_id") != "ag_new" || row.GetCredential("refresh_token") != "new-refresh" {
		t.Fatalf("updated row = generation %d family %q credentials %#v", row.CredentialGeneration, row.CredentialFamilyID, row.Credentials)
	}
	if _, applied, err := db.ReplaceAccountCredentialsCAS(ctx, id, 1, "ag_stale", map[string]any{"refresh_token": "stale"}); err != nil || applied {
		t.Fatalf("stale ReplaceAccountCredentialsCAS() applied=%v err=%v", applied, err)
	}
}

func TestUpdateAccountCredentialsCASKeepsEmbeddedFamilyCanonical(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "relay-family-refresh-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	id, err := db.InsertAccountWithUpstream(ctx, "relay", "relay", "oauth", map[string]interface{}{
		"upstream_type":        "relay",
		"refresh_token":        "old-refresh",
		"credential_family_id": "ag_canonical",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	newGeneration, applied, err := db.UpdateAccountCredentialsCAS(ctx, id, 1, map[string]any{
		"refresh_token":        "rotated-refresh",
		"credential_family_id": "ag_drifted",
	})
	if err != nil || !applied || newGeneration != 2 {
		t.Fatalf("UpdateAccountCredentialsCAS() = generation %d applied %v err %v", newGeneration, applied, err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.CredentialGeneration != 2 || row.CredentialFamilyID != "ag_canonical" || row.GetCredential("credential_family_id") != "ag_canonical" || row.GetCredential("refresh_token") != "rotated-refresh" {
		t.Fatalf("updated row = generation %d family %q credentials %#v", row.CredentialGeneration, row.CredentialFamilyID, row.Credentials)
	}
}

// 列表投影必须带出订阅筛选依赖的三个键，否则账号页"订阅状态"筛选会把全部账号判成未知。
func TestListAccountListProjectionCarriesSubscriptionKeys(t *testing.T) {
	ctx := context.Background()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "subscription-projection.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	id, err := db.InsertAccountWithCredentials(ctx, "sub-proj", map[string]interface{}{
		"plan_type":                "plus",
		"subscription_expires_at":  "2026-10-05T07:17:05Z",
		"subscription_sync_state":  "confirmed",
		"subscription_grace_until": "2026-10-12T00:00:00Z",
	}, "")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := db.ListAccountListProjection(ctx, "")
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		if row.GetCredential("subscription_expires_at") != "2026-10-05T07:17:05Z" ||
			row.GetCredential("subscription_sync_state") != "confirmed" ||
			row.GetCredential("subscription_grace_until") != "2026-10-12T00:00:00Z" {
			t.Fatalf("projection credentials = %v", row.Credentials)
		}
		return
	}
	t.Fatal("inserted account missing from projection")
}
