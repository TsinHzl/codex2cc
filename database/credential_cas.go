package database

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// Routine credential-refresh CAS helper. The channel-specific snapshot-stamp
// loop was dropped with its channel; the core generation CAS is
// channel-agnostic and backs the credential tests.
func (db *DB) UpdateAccountCredentialsCAS(ctx context.Context, accountID, expectedGeneration int64, updates map[string]any) (newGeneration int64, applied bool, err error) {
	err = db.withSQLiteWriteLock(ctx, func() error {
		tx, beginErr := db.conn.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback()
		query := `SELECT credentials, credential_generation, credential_family_id FROM accounts WHERE id=$1 AND status <> 'deleted'`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var raw any
		var current int64
		var familyID string
		if scanErr := tx.QueryRowContext(ctx, query, accountID).Scan(&raw, &current, &familyID); scanErr != nil {
			return scanErr
		}
		if current != expectedGeneration {
			newGeneration = current
			return nil
		}
		merged := mergeCredentialMaps(decodeCredentials(raw), updates)
		familyID = strings.TrimSpace(familyID)
		if familyID == "" {
			familyID = credentialFamilyCandidate(merged)
		}
		if familyID == "" {
			familyID = "cf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		// Keep the compatibility JSON field synchronized with the canonical
		// column in the same write that publishes the rotated credential.
		merged["credential_family_id"] = familyID
		encoded, marshalErr := json.Marshal(encryptSensitiveCredentials(merged))
		if marshalErr != nil {
			return marshalErr
		}
		updateQuery := `UPDATE accounts SET credentials=$1, credential_family_id=$2, credential_generation=credential_generation+1, updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND credential_generation=$4`
		if !db.isSQLite() {
			updateQuery = `UPDATE accounts SET credentials=$1::jsonb, credential_family_id=$2, credential_generation=credential_generation+1, updated_at=NOW() WHERE id=$3 AND credential_generation=$4`
		}
		res, execErr := tx.ExecContext(ctx, updateQuery, encoded, familyID, accountID, expectedGeneration)
		if execErr != nil {
			return execErr
		}
		n, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if n == 0 {
			return nil
		}
		applied = true
		newGeneration = expectedGeneration + 1
		return tx.Commit()
	})
	return
}

// ReplaceAccountCredentialsCAS atomically replaces an account identity.
func (db *DB) ReplaceAccountCredentialsCAS(ctx context.Context, accountID, expectedGeneration int64, familyID string, updates map[string]any) (newGeneration int64, applied bool, err error) {
	if db == nil || db.conn == nil || accountID <= 0 || expectedGeneration <= 0 || len(updates) == 0 {
		return 0, false, nil
	}
	err = db.withSQLiteWriteLock(ctx, func() error {
		tx, beginErr := db.conn.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer tx.Rollback()

		query := `SELECT credentials, credential_generation FROM accounts WHERE id=$1 AND status <> 'deleted'`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var raw any
		var current int64
		if scanErr := tx.QueryRowContext(ctx, query, accountID).Scan(&raw, &current); scanErr != nil {
			return scanErr
		}
		if current != expectedGeneration {
			newGeneration = current
			return nil
		}
		merged := mergeCredentialMaps(decodeCredentials(raw), updates)
		familyID = strings.TrimSpace(familyID)
		if familyID == "" {
			familyID = credentialFamilyCandidate(merged)
		}
		if familyID == "" {
			familyID = "cf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		merged["credential_family_id"] = familyID
		encoded, marshalErr := json.Marshal(encryptSensitiveCredentials(merged))
		if marshalErr != nil {
			return marshalErr
		}
		updateQuery := `UPDATE accounts SET credentials=$1, credential_family_id=$2, credential_generation=credential_generation+1, updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND credential_generation=$4`
		if !db.isSQLite() {
			updateQuery = `UPDATE accounts SET credentials=$1::jsonb, credential_family_id=$2, credential_generation=credential_generation+1, updated_at=NOW() WHERE id=$3 AND credential_generation=$4`
		}
		res, execErr := tx.ExecContext(ctx, updateQuery, encoded, familyID, accountID, expectedGeneration)
		if execErr != nil {
			return execErr
		}
		rows, rowsErr := res.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if rows == 0 {
			return nil
		}
		applied = true
		newGeneration = expectedGeneration + 1
		return tx.Commit()
	})
	return
}
