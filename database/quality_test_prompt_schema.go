package database

import (
	"context"
	"fmt"
)

// Extracted from quality_test_prompts.go; only the schema-ensure half is kept
// because the preset CRUD surface was removed with the other channels.
func (db *DB) ensureQualityTestPromptSchema(ctx context.Context) error {
	idType, timeType := "BIGSERIAL PRIMARY KEY", "TIMESTAMPTZ"
	if db.isSQLite() {
		idType, timeType = "INTEGER PRIMARY KEY AUTOINCREMENT", "TIMESTAMP"
	}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS quality_test_prompts (
		 id %s, name TEXT NOT NULL, prompt TEXT NOT NULL,
		 usage_count INTEGER NOT NULL DEFAULT 0, last_used_at %s,
		 created_at %s NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at %s NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`, idType, timeType, timeType, timeType),
		`CREATE INDEX IF NOT EXISTS idx_quality_test_prompts_updated ON quality_test_prompts(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
