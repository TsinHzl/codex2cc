package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex2cc/auth"
	"github.com/codex2cc/database"
	"github.com/gin-gonic/gin"
)

type scopedModelJSON struct {
	ID      string `json:"id"`
	Owner   string `json:"owned_by"`
	Created int64  `json:"created"`
}

func scopedModelByID(models []scopedModelJSON, id string) (string, int64, bool) {
	for _, model := range models {
		if model.ID == id {
			return model.Owner, model.Created, true
		}
	}
	return "", 0, false
}

func listScopedModelsForTest(t *testing.T, handler *Handler, row *database.APIKeyRow) []scopedModelJSON {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(contextAPIKeyRow, row)
	handler.ListModels(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data []scopedModelJSON `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	return payload.Data
}

// TestScopedModelsAppliesKeyAccountAndHardGrokGates、
// TestScopedModelsIgnoresTransientCooldownButHonorsPaused、
// TestScopedModelsPlanMissingFailsClosedAndEmptyIs200、
// TestScopedModelsAliasesOwnersFirstSeenAndDeterministicOrder、
// TestScopedCodexManifestAccountHonorsGrokOnlyChannel、
// TestScopedModelRecordsDoesNotNeedDatabase、
// TestScopedModelsDeclaredListCannotOverrideCatalogVisibility 随 grok 上游、
// TestScopedModelsIncludeAntigravityAccounts 随 antigravity 上游、
// TestScopedModelsClaudeOnlyKeyDoesNotExposeCodexAliases 随 claude 原生上游
// 一起从 codex2cc 裁剪：上述账号类型与 Grok 媒体模型目录（isGrokMediaModel）
// 均不再存在。

func TestFilterCodexManifestRetainsUnknownFieldsAndStableETag(t *testing.T) {
	body := []byte(`{"models":[{"slug":"gpt-a","display_name":"A"},{"slug":"gpt-b","new_field":{"x":1}}],"future":true}`)
	filtered, etag, err := filterCodexManifest(body, `W/"upstream"`, func(slug string) bool { return slug == "gpt-b" })
	if err != nil {
		t.Fatalf("filterCodexManifest: %v", err)
	}
	if etag == "" {
		t.Fatal("gateway ETag is empty")
	}
	var payload struct {
		Models []map[string]any `json:"models"`
		Future bool             `json:"future"`
	}
	if err := json.Unmarshal(filtered, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Future || len(payload.Models) != 1 || payload.Models[0]["slug"] != "gpt-b" || payload.Models[0]["new_field"] == nil {
		t.Fatalf("filtered payload lost schema fields: %s", filtered)
	}
	filteredAgain, etagAgain, err := filterCodexManifest(body, `W/"upstream"`, func(slug string) bool { return slug == "gpt-b" })
	if err != nil || string(filteredAgain) != string(filtered) || etagAgain != etag {
		t.Fatalf("filtered manifest is unstable: err=%v etag=%q/%q", err, etag, etagAgain)
	}
}
func TestFilterCodexManifestUnknownSchemaFailsClosed(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"data":[]}`), []byte(`{"models":[{"id":"gpt-a"}]}`), []byte(`not json`)} {
		if _, _, err := filterCodexManifest(body, "", func(string) bool { return true }); err == nil {
			t.Fatalf("filterCodexManifest(%s) accepted unsafe schema", body)
		}
	}
}
func TestScopedModelsModelAllowAppliesToAliasName(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	store.AddAccount(&auth.Account{DBID: 1, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://relay.example", APIKey: "sk", Models: []string{"target"}, ModelMapping: `{"public-alias":"target"}`})
	handler := NewHandler(store, nil, nil, nil)
	models := listScopedModelsForTest(t, handler, &database.APIKeyRow{ID: 1, Limits: database.APIKeyLimits{ModelAllow: []string{"public-alias"}}})
	if len(models) != 1 || models[0].ID != "public-alias" || models[0].Owner != "codex2cc" {
		t.Fatalf("models = %+v, want alias only", models)
	}
}
