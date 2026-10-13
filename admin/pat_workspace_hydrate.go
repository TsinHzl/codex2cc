package admin

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/codex2cc/auth"
)

// patWorkspaceHydrateBackoff 是 whoami 补全失败后的重试间隔。失败多半是 token 本身
// 没有 whoami 权限，短时间重试不会变好，按小时级探针一天打几次足够。
const patWorkspaceHydrateBackoff = 6 * time.Hour

// needsPATWorkspaceHydration 识别还没拿到工作区 ID 的纯 AT 账号：文件 / 批量 / 自助
// 导入与升级前的存量 PAT 都没经过粘贴入口的 whoami 补全，wham 统计对它们一直关着。
func needsPATWorkspaceHydration(account *auth.Account) bool {
	if account == nil || account.DBID <= 0 {
		return false
	}
	if !whamDailyUsageChannelSupported(account) {
		return false
	}
	if accessTokenTypeForToken(account.GetAccessToken()) != accessTokenTypeCodexAT {
		return false
	}
	return strings.TrimSpace(account.EffectiveAccountID()) == ""
}

// hydratePATWorkspace 为单个缺工作区的 PAT 账号调 whoami 并把身份写回内存与库。
// force=true 跳过失败退避（手动刷新）。返回是否已具备工作区 ID。
func (h *Handler) hydratePATWorkspace(ctx context.Context, account *auth.Account, force bool) bool {
	if !needsPATWorkspaceHydration(account) {
		return account != nil && strings.TrimSpace(account.EffectiveAccountID()) != ""
	}
	if h == nil || h.store == nil {
		return false
	}
	id := account.DBID
	if !force {
		h.whamDailyBackfillMu.Lock()
		failedAt, backedOff := h.patWhoAmIFailedAt[id]
		h.whamDailyBackfillMu.Unlock()
		if backedOff && time.Since(failedAt) < patWorkspaceHydrateBackoff {
			return false
		}
	}
	whoamiCtx, cancel := context.WithTimeout(ctx, patWhoAmIHydrateTimeout)
	defer cancel()
	meta, err := QueryPersonalAccessTokenMetadata(whoamiCtx, account.GetAccessToken(), h.store.ResolveProxyForAccount(account))
	if err == nil && (meta == nil || meta.ChatGPTAccountID == "") {
		err = errPATWhoAmINoWorkspace
	}
	if err != nil {
		h.whamDailyBackfillMu.Lock()
		if h.patWhoAmIFailedAt == nil {
			h.patWhoAmIFailedAt = map[int64]time.Time{}
		}
		h.patWhoAmIFailedAt[id] = time.Now()
		h.whamDailyBackfillMu.Unlock()
		log.Printf("PAT 账号 %d whoami 工作区补全失败: %v", id, err)
		return false
	}
	h.whamDailyBackfillMu.Lock()
	delete(h.patWhoAmIFailedAt, id)
	h.whamDailyBackfillMu.Unlock()
	email := ""
	if meta.Email != nil {
		email = strings.TrimSpace(*meta.Email)
	}
	h.store.UpdateAccountIdentity(account, email, meta.ChatGPTAccountID)
	if meta.ChatGPTPlanType != "" {
		h.store.UpdateAccountPlanType(account, meta.ChatGPTPlanType)
	}
	log.Printf("PAT 账号 %d 已通过 whoami 补全工作区 %s", id, auth.HashAccountID(meta.ChatGPTAccountID))
	return true
}

// hydratePATWorkspaces 在每轮小时探针前批量补全缺工作区的 PAT，补上的账号本轮即可
// 进入官方用量候选。并发与探针一致，避免号池大时一口气打爆 whoami。
func (h *Handler) hydratePATWorkspaces(ctx context.Context) {
	if h == nil || h.store == nil {
		return
	}
	var pending []*auth.Account
	for _, account := range h.store.Accounts() {
		if needsPATWorkspaceHydration(account) {
			pending = append(pending, account)
		}
	}
	if len(pending) == 0 {
		return
	}
	sem := make(chan struct{}, whamDailyUsageProbeConcurrency)
	var wg sync.WaitGroup
	for _, account := range pending {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(acc *auth.Account) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			h.hydratePATWorkspace(ctx, acc, false)
		}(account)
	}
	wg.Wait()
}
