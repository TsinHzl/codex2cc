package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAPIKeyAuthRevisionSQLite(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testAPIKeyAuthRevision(t, db)
}

func TestAPIKeyAuthRevisionPostgres(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated PostgreSQL database")
	}
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testAPIKeyAuthRevision(t, db)
}

func testAPIKeyAuthRevision(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	before, err := db.GetAPIKeyAuthRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertAPIKey(ctx, "auth-revision", fmt.Sprintf("sk-auth-revision-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteAPIKey(ctx, id) })
	created, err := db.GetAPIKeyAuthRevision(ctx)
	if err != nil || created.Generation != before.Generation+1 || created.KeyCount != before.KeyCount+1 {
		t.Fatalf("insert revision: %+v -> %+v, %v", before, created, err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE api_keys SET quota_used=3,total_used=5 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	used, state, err := db.GetAPIKeyAuthQuota(ctx, id)
	if err != nil || used != 3 || state != created {
		t.Fatalf("usage changed configuration revision: used=%v state=%+v err=%v", used, state, err)
	}
	for _, update := range []APIKeyUpdate{
		{EnabledSet: true, Enabled: false}, {NameSet: true, Name: "renamed"},
		{QuotaLimitSet: true, QuotaLimit: 10}, {AllowedGroupIDsSet: true, AllowedGroupIDs: []int64{7}},
		{LimitsSet: true, Limits: APIKeyLimits{ModelAllow: []string{"gpt-5.4"}}},
		{ExpiresAtSet: true, ExpiresAt: sql.NullTime{Valid: true, Time: time.Now().UTC().Add(time.Hour)}},
	} {
		old, _ := db.GetAPIKeyAuthRevision(ctx)
		if err := db.UpdateAPIKey(ctx, id, update); err != nil {
			t.Fatal(err)
		}
		next, err := db.GetAPIKeyAuthRevision(ctx)
		if err != nil || next.Generation != old.Generation+1 || next.KeyCount != old.KeyCount {
			t.Fatalf("metadata update did not advance revision: %+v %+v %v", old, next, err)
		}
	}
	stable, _ := db.GetAPIKeyAuthRevision(ctx)
	if _, err := db.conn.ExecContext(ctx, `UPDATE api_keys SET name=name WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAPIKeyAuthRevision(ctx); err != nil || got != stable {
		t.Fatalf("no-op invalidated cache: %+v %v", got, err)
	}
	rollback := errors.New("rollback")
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET enabled=TRUE WHERE id=$1`, id); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got, err := db.GetAPIKeyAuthRevision(ctx); err != nil || got != stable {
		t.Fatalf("rolled-back revision escaped: %+v %v", got, err)
	}
	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := db.UpdateAPIKeyName(ctx, id, fmt.Sprintf("writer-%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got, err := db.GetAPIKeyAuthRevision(ctx); err != nil || got.Generation != stable.Generation+writers {
		t.Fatalf("concurrent revision lost: %+v %v", got, err)
	}
	if err := db.DeleteAPIKey(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAPIKeyAuthRevision(ctx); err != nil || got.KeyCount != before.KeyCount {
		t.Fatalf("delete count: %+v %v", got, err)
	}
	if _, _, err := db.GetAPIKeyAuthQuota(ctx, id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted key has quota: %v", err)
	}
}

func TestAPIKeyAuthRevisionDatabaseScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	first, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	other, err := New("sqlite", filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, _ := first.GetAPIKeyAuthRevision(context.Background())
	b, _ := second.GetAPIKeyAuthRevision(context.Background())
	c, _ := other.GetAPIKeyAuthRevision(context.Background())
	if a.Namespace != b.Namespace || a.Namespace == c.Namespace {
		t.Fatal("database cache scopes are not isolated and stable")
	}
}
