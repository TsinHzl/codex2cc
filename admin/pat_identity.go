package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/codex2cc/auth"
)

const (
	openAIAuthAccountsBaseURL    = "https://auth.openai.com/api/accounts"
	personalAccessTokenWhoAmIURL = openAIAuthAccountsBaseURL + "/v1/user-auth-credential/whoami"
	patWhoAmITimeout             = 10 * time.Second
)

// patWhoAmIURLForTest 允许测试替换 whoami 端点 URL。生产代码不要赋值。
var patWhoAmIURLForTest = ""

// errPATWhoAmINoWorkspace 表示 whoami 成功但没给出工作区 ID。
var errPATWhoAmINoWorkspace = errors.New("whoami response has no chatgpt_account_id")

// PersonalAccessTokenMetadata 是 /v1/user-auth-credential/whoami 返回的身份元数据结构。
type PersonalAccessTokenMetadata struct {
	Email                   *string `json:"email"`
	ChatGPTUserID           string  `json:"chatgpt_user_id"`
	ChatGPTAccountID        string  `json:"chatgpt_account_id"`
	ChatGPTPlanType         string  `json:"chatgpt_plan_type"`
	ChatGPTAccountIsFedramp bool    `json:"chatgpt_account_is_fedramp"`
}

// patWhoAmIStatusError 表示 whoami 端点返回了非 2xx：这是这枚 token 自己的问题
// （无权限 / 已吊销），不代表网络不通，批量导入时不应因此熔断后续 token。
type patWhoAmIStatusError struct {
	StatusCode int
}

func (e *patWhoAmIStatusError) Error() string {
	return fmt.Sprintf("whoami returned status %d", e.StatusCode)
}

// newPATWhoAmIClient 复用按代理池化的严格客户端（连接复用、代理配置失败不静默绕过），
// 只覆盖超时与重定向策略：带 Bearer PAT 的请求绝不自动跟随重定向发往别的 host。
func newPATWhoAmIClient(proxyURL string) (*http.Client, error) {
	base, err := auth.BuildHTTPClientChecked(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL for whoami: %w", err)
	}
	client := *base
	client.Timeout = patWhoAmITimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

// QueryPersonalAccessTokenMetadata 调用 OpenAI Auth API 的 /v1/user-auth-credential/whoami
// 获取 PAT 的 workspace ID (ChatGPT-Account-Id)、用户 ID、邮箱和套餐类型。
func QueryPersonalAccessTokenMetadata(ctx context.Context, accessToken string, proxyURL string) (*PersonalAccessTokenMetadata, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return nil, errors.New("access token is empty")
	}

	endpoint := personalAccessTokenWhoAmIURL
	if patWhoAmIURLForTest != "" {
		endpoint = patWhoAmIURLForTest
	}

	client, err := newPATWhoAmIClient(proxyURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create whoami request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex2api")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute whoami request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &patWhoAmIStatusError{StatusCode: resp.StatusCode}
	}

	var meta PersonalAccessTokenMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode whoami response: %w", err)
	}

	meta.ChatGPTAccountID = strings.TrimSpace(meta.ChatGPTAccountID)
	meta.ChatGPTUserID = strings.TrimSpace(meta.ChatGPTUserID)
	meta.ChatGPTPlanType = strings.TrimSpace(meta.ChatGPTPlanType)

	return &meta, nil
}
