package auth

import "testing"

func TestResinEgressExemptsCodexAccountsFromPoolFailClosed(t *testing.T) {
	const disabledURL = "http://disabled.example:8080"
	store := &Store{
		proxyPoolEnabled: true,
		proxyPool:        nil,
		proxyPoolSet:     buildProxyPoolSet(nil),
		managedProxySet:  buildProxyPoolSet([]string{disabledURL}),
	}
	old := ResinEgressEnabled()
	t.Cleanup(func() { SetResinEgressEnabled(old) })

	unbound := &Account{DBID: 1}
	pinned := &Account{DBID: 2, ProxyURL: disabledURL}
	relay := &Account{DBID: 3, UpstreamType: UpstreamOpenAIResponses, BaseURL: "https://relay.example", APIKey: "sk-test"}

	SetResinEgressEnabled(false)
	if store.AccountHasUsableEgress(unbound) || store.AccountHasUsableEgress(pinned) {
		t.Fatal("without resin an empty pool must stay fail-closed")
	}

	SetResinEgressEnabled(true)
	if !store.AccountHasUsableEgress(unbound) {
		t.Fatal("resin carries codex egress: empty pool must not block an unbound codex account")
	}
	if !store.AccountHasUsableEgress(pinned) {
		t.Fatal("resin carries codex egress: a disabled managed pin must not block a codex account")
	}
	if got := store.ResolveProxyForAccount(pinned); got != "" {
		t.Fatalf("resolved proxy for disabled pin = %q, want empty (resin ignores it anyway)", got)
	}
	if !relay.IsOpenAIResponsesAPI() {
		t.Fatal("fixture must be relay-style")
	}
	if store.AccountHasUsableEgress(relay) {
		t.Fatal("relay-style accounts do not go through resin and must stay fail-closed")
	}
}
