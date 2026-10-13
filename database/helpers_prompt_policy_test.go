package database

import (
	"path/filepath"
	"testing"
)

// Extracted from prompt_policy_incident_test.go; the general-purpose SQLite test
// fixture outlived the promptfilter removal because usage tests still use it.
func newPromptPolicySQLiteTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New("sqlite", filepath.Join(t.TempDir(), "prompt-policy.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
