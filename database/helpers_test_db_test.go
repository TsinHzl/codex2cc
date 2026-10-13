package database

import (
	"path/filepath"
	"testing"
)

// Shared SQLite test fixture for account-health and schema tests.
func newTestDBFixture(t *testing.T) *DB {
	t.Helper()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "test-state.db"))
	if err != nil {
		t.Fatalf("New sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
