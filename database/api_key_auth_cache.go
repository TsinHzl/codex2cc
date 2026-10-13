package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

func apiKeyAuthDatabaseScope(driver, dsn, schema string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(driver+"\x00"+dsn+"\x00"+schema)))
}

// APIKeyAuthRevision is committed atomically with authentication configuration.
// A single counter orders commits; MAX(outbox.id) cannot do this because sequence
// IDs can become visible out of order. Usage-only writes do not advance it.
type APIKeyAuthRevision struct {
	Namespace  string
	Generation int64
	KeyCount   int
}

func (db *DB) GetAPIKeyAuthRevision(ctx context.Context) (APIKeyAuthRevision, error) {
	var state APIKeyAuthRevision
	err := db.conn.QueryRowContext(ctx, `SELECT namespace,generation,key_count FROM api_key_auth_cache_state WHERE id=1`).Scan(&state.Namespace, &state.Generation, &state.KeyCount)
	if err == nil {
		state.Namespace = db.authCacheScope + ":" + state.Namespace
	}
	return state, err
}

// GetAPIKeyAuthQuota never serves a cached balance. The revision is read in the
// same statement so a limit change/deletion cannot reuse a former configuration.
func (db *DB) GetAPIKeyAuthQuota(ctx context.Context, id int64) (float64, APIKeyAuthRevision, error) {
	var used float64
	var state APIKeyAuthRevision
	err := db.conn.QueryRowContext(ctx, `SELECT COALESCE(k.quota_used,0),s.namespace,s.generation,s.key_count
		FROM api_keys k CROSS JOIN api_key_auth_cache_state s WHERE k.id=$1 AND s.id=1`, id).Scan(&used, &state.Namespace, &state.Generation, &state.KeyCount)
	if err == nil {
		state.Namespace = db.authCacheScope + ":" + state.Namespace
	}
	return used, state, err
}

func (db *DB) ensureAPIKeyAuthCacheSchema(ctx context.Context) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if !db.isSQLite() {
			if _, err := tx.ExecContext(ctx, `LOCK TABLE api_keys IN SHARE ROW EXCLUSIVE MODE`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS api_key_auth_cache_state (
			id INTEGER PRIMARY KEY CHECK(id=1), namespace TEXT NOT NULL,
			generation BIGINT NOT NULL DEFAULT 1, key_count BIGINT NOT NULL DEFAULT 0
		)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_key_auth_cache_state(id,namespace,generation,key_count)
			SELECT 1,$1,1,COUNT(*) FROM api_keys WHERE TRUE ON CONFLICT(id) DO NOTHING`, uuid.NewString()); err != nil {
			return err
		}
		if db.isSQLite() {
			_, err := tx.ExecContext(ctx, `
			CREATE TRIGGER IF NOT EXISTS api_key_auth_cache_insert AFTER INSERT ON api_keys BEGIN
				UPDATE api_key_auth_cache_state SET generation=generation+1,key_count=key_count+1 WHERE id=1;
			END;
			CREATE TRIGGER IF NOT EXISTS api_key_auth_cache_delete AFTER DELETE ON api_keys BEGIN
				UPDATE api_key_auth_cache_state SET generation=generation+1,key_count=key_count-1 WHERE id=1;
			END;
			CREATE TRIGGER IF NOT EXISTS api_key_auth_cache_update AFTER UPDATE OF name,key,enabled,quota_limit,expires_at,allowed_group_ids,limits ON api_keys
			WHEN OLD.name IS NOT NEW.name OR OLD.key IS NOT NEW.key OR OLD.enabled IS NOT NEW.enabled
			 OR OLD.quota_limit IS NOT NEW.quota_limit OR OLD.expires_at IS NOT NEW.expires_at
			 OR OLD.allowed_group_ids IS NOT NEW.allowed_group_ids OR OLD.limits IS NOT NEW.limits
			BEGIN UPDATE api_key_auth_cache_state SET generation=generation+1 WHERE id=1; END;`)
			return err
		}
		_, err := tx.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION codex2api_api_key_auth_revision() RETURNS TRIGGER AS $$
		BEGIN
			IF TG_OP = 'TRUNCATE' THEN
				UPDATE api_key_auth_cache_state SET generation=generation+1,key_count=0 WHERE id=1;
			ELSIF TG_OP = 'INSERT' THEN
				UPDATE api_key_auth_cache_state SET generation=generation+1,key_count=key_count+1 WHERE id=1;
			ELSIF TG_OP = 'DELETE' THEN
				UPDATE api_key_auth_cache_state SET generation=generation+1,key_count=key_count-1 WHERE id=1;
			ELSIF OLD.name IS DISTINCT FROM NEW.name OR OLD.key IS DISTINCT FROM NEW.key OR OLD.enabled IS DISTINCT FROM NEW.enabled
			 OR OLD.quota_limit IS DISTINCT FROM NEW.quota_limit OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
			 OR OLD.allowed_group_ids IS DISTINCT FROM NEW.allowed_group_ids OR OLD.limits IS DISTINCT FROM NEW.limits THEN
				UPDATE api_key_auth_cache_state SET generation=generation+1 WHERE id=1;
			END IF;
			RETURN NULL;
		END; $$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS api_key_auth_cache_change ON api_keys;
		CREATE TRIGGER api_key_auth_cache_change AFTER INSERT OR DELETE OR UPDATE OF name,key,enabled,quota_limit,expires_at,allowed_group_ids,limits
			ON api_keys FOR EACH ROW EXECUTE FUNCTION codex2api_api_key_auth_revision();
		DROP TRIGGER IF EXISTS api_key_auth_cache_truncate ON api_keys;
		CREATE TRIGGER api_key_auth_cache_truncate AFTER TRUNCATE ON api_keys FOR EACH STATEMENT EXECUTE FUNCTION codex2api_api_key_auth_revision();`)
		if err != nil {
			return fmt.Errorf("install API key authentication revision trigger: %w", err)
		}
		return nil
	})
}
