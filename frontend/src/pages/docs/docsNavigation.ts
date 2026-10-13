export type DocEntry = {
  id: string;
  section: string;
  title: string;
  summary: string;
  method?: string;
};

export function resolveDocsTarget(hash: string, entries: DocEntry[]) {
  let id = hash.replace(/^#/, "");
  try {
    id = decodeURIComponent(id);
  } catch {
    id = "";
  }
  const aliases: Record<string, string> = {
    "client-config": "qs-tools",
    "client-codex": "qs-tools",
    "client-claude": "qs-tools",
  };
  const entry = entries.find((entry) => entry.id === (aliases[id] || id));
  return {
    id: entry?.id || "quick-start",
    section: entry?.section || "quick-start",
    client:
      id === "client-claude"
        ? "claude-code"
        : id === "client-codex"
          ? "codex-cli"
          : undefined,
  };
}

export function searchDocEntries(query: string, entries: DocEntry[]) {
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [];
  return entries
    .filter((entry) =>
      words.every((word) =>
        `${entry.title} ${entry.summary} ${entry.method || ""}`
          .toLocaleLowerCase()
          .includes(word),
      ),
    )
    .slice(0, 20);
}
