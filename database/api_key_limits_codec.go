package database

import (
	"encoding/json"
	"strings"
)

func decodeAPIKeyLimits(raw interface{}) APIKeyLimits {
	data := bytesFromDBValue(raw)
	if len(data) == 0 {
		return APIKeyLimits{}
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return APIKeyLimits{}
	}
	var out APIKeyLimits
	if err := json.Unmarshal(data, &out); err != nil {
		return APIKeyLimits{}
	}
	return out
}

func encodeAPIKeyLimits(l APIKeyLimits) string {
	if l.IsZero() {
		return "{}"
	}
	b, err := json.Marshal(l)
	if err != nil {
		return "{}"
	}
	return string(b)
}
