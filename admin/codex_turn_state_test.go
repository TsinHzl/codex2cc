package admin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
)

func TestParseAccountSchedulerUpdateCodexTurnState(t *testing.T) {
	update, err := parseAccountSchedulerUpdate(updateAccountSchedulerReq{
		CodexTurnState:       json.RawMessage(`"  state-one "`),
		CodexTurnStateModels: json.RawMessage(`" GPT-5.5, gpt-5* ,gpt-5.5"`),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !update.hasChanges() {
		t.Fatal("turn state fields must count as changes")
	}
	if got := update.CredentialUpdates[auth.CodexTurnStateCredentialKey]; got != "state-one" {
		t.Fatalf("value credential = %#v", got)
	}
	if got := update.CredentialUpdates[auth.CodexTurnStateModelsCredentialKey]; got != "gpt-5.5, gpt-5*" {
		t.Fatalf("models credential = %#v", got)
	}
	setAt, _ := update.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey].(string)
	if auth.ParseCodexTurnStateSetAt(setAt).IsZero() {
		t.Fatalf("set_at must default to now for a fresh value, got %q", setAt)
	}

	cleared, err := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnState: json.RawMessage(`null`)})
	if err != nil {
		t.Fatalf("parse null: %v", err)
	}
	if got := cleared.CredentialUpdates[auth.CodexTurnStateCredentialKey]; got != "" {
		t.Fatalf("null must clear the value, got %#v", got)
	}
	if got := cleared.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey]; got != "" {
		t.Fatalf("clearing must reset set_at, got %#v", got)
	}

	modelsOnly, err := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnStateModels: json.RawMessage(`"gpt-5"`)})
	if err != nil {
		t.Fatalf("parse models only: %v", err)
	}
	if _, touched := modelsOnly.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey]; touched {
		t.Fatal("models-only edit must not touch set_at")
	}

	if _, err := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnState: json.RawMessage(`"a\nb"`)}); err == nil {
		t.Fatal("multi-line value must be rejected")
	}
}

// 时效起点只在注入值真正换掉时重置：原样重提同一个值保留旧起点，存量行没有起点时补一次。
func TestRefineCodexTurnStateSetAt(t *testing.T) {
	previous := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	row := &database.AccountRow{Credentials: map[string]interface{}{
		auth.CodexTurnStateCredentialKey:      "state-one",
		auth.CodexTurnStateSetAtCredentialKey: previous,
	}}

	same, _ := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnState: json.RawMessage(`"state-one"`)})
	refineCodexTurnStateSetAt(row, same)
	if _, touched := same.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey]; touched {
		t.Fatal("re-submitting the same value must keep the old set_at")
	}

	replaced, _ := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnState: json.RawMessage(`"state-two"`)})
	refineCodexTurnStateSetAt(row, replaced)
	if got, _ := replaced.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey].(string); got == "" || got == previous {
		t.Fatalf("replacing the value must reset set_at, got %q", got)
	}

	legacy := &database.AccountRow{Credentials: map[string]interface{}{auth.CodexTurnStateCredentialKey: "state-one"}}
	backfill, _ := parseAccountSchedulerUpdate(updateAccountSchedulerReq{CodexTurnState: json.RawMessage(`"state-one"`)})
	refineCodexTurnStateSetAt(legacy, backfill)
	if got, _ := backfill.CredentialUpdates[auth.CodexTurnStateSetAtCredentialKey].(string); got == "" {
		t.Fatal("legacy row without set_at must be backfilled on re-save")
	}
}
