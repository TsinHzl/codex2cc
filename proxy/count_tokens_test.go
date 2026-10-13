package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 验证本地 token 计数兼容端点（/v1/messages/count_tokens 与
// /v1/responses/input_tokens）：本地估算返回正数 input_tokens，不触达上游、
// 不依赖账号池；零值 Handler 即可，因本地估算不依赖任何 Handler 状态。
func TestLocalTokenCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	r := gin.New()
	r.POST("/v1/messages/count_tokens", h.CountTokens)
	r.POST("/v1/responses/input_tokens", h.ResponsesInputTokens)

	cases := []struct {
		name    string
		body    string
		path    string
		wantST  int
		wantMsg string
	}{
		{name: "anthropic 正常估算", path: "/v1/messages/count_tokens", body: `{"model":"gpt-5.5","system":"you are helpful","messages":[{"role":"user","content":"hello"}]}`, wantST: http.StatusOK},
		{name: "openai 正常估算", path: "/v1/responses/input_tokens", body: `{"model":"gpt-5.5","instructions":"be brief","input":"hello world"}`, wantST: http.StatusOK},
		{name: "anthropic 非法 JSON", path: "/v1/messages/count_tokens", body: `{invalid`, wantST: http.StatusBadRequest, wantMsg: "invalid_request"},
		{name: "openai 非法 JSON", path: "/v1/responses/input_tokens", body: `{invalid`, wantST: http.StatusBadRequest, wantMsg: "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != tc.wantST {
				t.Fatalf("status = %d, want %d, body=%s", resp.Code, tc.wantST, resp.Body.String())
			}
			if tc.wantMsg != "" {
				if !strings.Contains(resp.Body.String(), tc.wantMsg) {
					t.Fatalf("error body should contain %q, got %s", tc.wantMsg, resp.Body.String())
				}
				return
			}
			var out struct {
				InputTokens int `json:"input_tokens"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if out.InputTokens <= 0 {
				t.Fatalf("input_tokens = %d, want > 0", out.InputTokens)
			}
		})
	}
}
