package database

import (
	"strings"
	"time"
)

func parsePromptRiskTimeValue(value any) (time.Time, error) {
	parsed, err := parseDBTimeValue(value)
	if err == nil {
		return parsed, nil
	}
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return time.Time{}, err
	}
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
	} {
		if parsed, parseErr := time.Parse(layout, raw); parseErr == nil {
			return parsed, nil
		}
	}
	return time.Time{}, err
}
