package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// GetAPIKeyByID.
func (db *DB) GetAPIKeyByID(ctx context.Context, id int64) (*APIKeyRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT `+apiKeySelectColumns+` FROM api_keys WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	return scanAPIKeyRow(rows)
}

func scanAPIKeyRow(scanner interface {
	Scan(dest ...interface{}) error
}) (*APIKeyRow, error) {
	row := &APIKeyRow{}
	var createdAtRaw, expiresAtRaw, lastResetAtRaw, allowedGroupsRaw, limitsRaw interface{}
	if err := scanner.Scan(&row.ID, &row.Name, &row.Key, &createdAtRaw, &row.QuotaLimit, &row.QuotaUsed, &row.TotalUsed, &row.ResetCount, &lastResetAtRaw, &expiresAtRaw, &allowedGroupsRaw, &limitsRaw, &row.Enabled); err != nil {
		return nil, err
	}
	createdAt, err := parseDBTimeValue(createdAtRaw)
	if err != nil {
		return nil, fmt.Errorf("解析 API Key 创建时间失败: %w", err)
	}
	expiresAt, err := parseDBNullTimeValue(expiresAtRaw)
	if err != nil {
		return nil, fmt.Errorf("解析 API Key 过期时间失败: %w", err)
	}
	lastResetAt, err := parseDBNullTimeValue(lastResetAtRaw)
	if err != nil {
		return nil, fmt.Errorf("解析 API Key 重置时间失败: %w", err)
	}
	row.CreatedAt = createdAt
	row.ExpiresAt = expiresAt
	row.LastResetAt = lastResetAt
	row.AllowedGroupIDs = decodeInt64SliceValue(allowedGroupsRaw)
	row.Limits = decodeAPIKeyLimits(limitsRaw)
	return row, nil
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func credentialFamilyCandidate(credentials map[string]any) string {
	if explicit, _ := credentials["credential_family_id"].(string); strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := credentials[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	isRelay := strings.EqualFold(get("upstream_type"), "relay")
	authKind := "oauth"
	if get("api_key") != "" {
		authKind = "api_key"
	}
	issuer := strings.ToLower(strings.TrimRight(get("oidc_issuer"), "/"))
	origin := get("base_url")
	if parsed, err := url.Parse(origin); err == nil && parsed.Hostname() != "" {
		host := strings.ToLower(parsed.Hostname())
		if port := parsed.Port(); port != "" && port != "443" && port != "80" {
			host += ":" + port
		}
		origin = strings.ToLower(parsed.Scheme) + "://" + host
	} else {
		origin = strings.ToLower(strings.TrimRight(origin, "/"))
	}
	principal := get("principal_id", "account_id", "chatgpt_account_id", "user_id")
	identity := strings.Join([]string{
		"credential-family-v1", authKind, issuer, get("client_id"),
		strings.ToLower(get("principal_type")), principal, origin,
	}, "\x00")
	if !isRelay && strings.TrimSpace(principal) == "" && get("api_key", "refresh_token", "access_token") == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}
