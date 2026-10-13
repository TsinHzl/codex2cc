import { useEffect, useState } from "react";
import { Search, X, ArrowUpRight, BookOpen, ChevronRight } from "lucide-react";
import { CodeBlock } from "./EndpointDoc";
import { searchDocEntries, type DocEntry } from "./docsNavigation";
import type { GuideSpec } from "./docsGuides";
import type { DocsLocale } from "./quickStartTools";

export function DocsSearch({
  entries,
  locale,
}: {
  entries: DocEntry[];
  locale: DocsLocale;
}) {
  const [query, setQuery] = useState("");
  const [focused, setFocused] = useState(false);
  const zh = locale === "zh";
  const results = searchDocEntries(query, entries);
  return (
    <div
      className="docs-search"
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node))
          setFocused(false);
      }}
    >
      <Search className="size-4 shrink-0 text-muted-foreground" />
      <input
        type="search"
        aria-label={zh ? "搜索文档" : "Search documentation"}
        placeholder={
          zh
            ? "搜索接口、参数或错误码…"
            : "Search endpoints, parameters or errors…"
        }
        value={query}
        onFocus={() => setFocused(true)}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            setQuery("");
            setFocused(false);
          }
          if (e.key === "ArrowDown") {
            e.preventDefault();
            e.currentTarget.parentElement
              ?.querySelector<HTMLAnchorElement>(".docs-search-results a")
              ?.focus();
          }
        }}
      />
      {query && (
        <button
          type="button"
          aria-label={zh ? "清除搜索" : "Clear search"}
          onClick={() => setQuery("")}
        >
          <X className="size-4" />
        </button>
      )}
      {focused && query.trim() && (
        <div className="docs-search-results">
          <div className="docs-search-count" role="status">
            {zh
              ? `找到 ${results.length} 项${results.length === 20 ? "（最多显示 20 项）" : ""}`
              : `${results.length} results${results.length === 20 ? " (showing up to 20)" : ""}`}
          </div>
          {results.length ? (
            results.map((entry) => (
              <a
                key={entry.id}
                href={`#${entry.id}`}
                onClick={() => {
                  setQuery("");
                  setFocused(false);
                }}
              >
                <span>
                  <strong>{entry.title}</strong>
                  <small>{entry.summary}</small>
                </span>
                <ArrowUpRight className="size-4 shrink-0" />
              </a>
            ))
          ) : (
            <p>
              {zh
                ? "试试模型名、/v1 路径、429 或上下文。"
                : "Try a model, /v1 path, 429 or context."}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

export function GuideArticle({
  guide,
  locale,
  activeId,
}: {
  guide: GuideSpec;
  locale: DocsLocale;
  activeId: string;
}) {
  const [expanded, setExpanded] = useState(activeId === guide.id);
  useEffect(() => {
    if (activeId === guide.id) setExpanded(true);
  }, [activeId, guide.id]);
  return (
    <article id={guide.id} className="docs-guide">
      <div className="docs-guide-heading">
        <div>
          <h3>
            <a href={`#${guide.id}`}>{guide.title}</a>
          </h3>
          <p>{guide.summary}</p>
        </div>
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={`${guide.id}-body`}
          onClick={() => setExpanded(!expanded)}
          className="docs-disclosure"
          aria-label={`${expanded ? (locale === "zh" ? "收起" : "Collapse") : locale === "zh" ? "展开" : "Expand"} ${guide.title}`}
        >
          <ChevronRight className={`size-4 ${expanded ? "rotate-90" : ""}`} />
        </button>
      </div>
      {expanded && (
        <div id={`${guide.id}-body`} className="docs-guide-body">
          {guide.paragraphs?.map((paragraph, i) => (
            <p key={i}>{paragraph}</p>
          ))}
          {guide.table && (
            <div className="docs-table-wrap">
              <table>
                <thead>
                  <tr>
                    {guide.table.headers.map((h) => (
                      <th key={h} scope="col">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {guide.table.rows.map((row, i) => (
                    <tr key={i}>
                      {row.map((cell, j) => (
                        <td key={j}>{cell}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {guide.examples?.map((example) => (
            <CodeBlock key={example.label} {...example} />
          ))}
          {!!guide.links?.length && (
            <div className="docs-guide-links">
              {guide.links.map((link) => (
                <a
                  key={link.href}
                  href={link.href}
                  {...(link.href.startsWith("https:")
                    ? { target: "_blank", rel: "noreferrer" }
                    : {})}
                >
                  <BookOpen className="size-3.5" />
                  {link.label}
                  <ArrowUpRight className="size-3" />
                </a>
              ))}
            </div>
          )}
        </div>
      )}
    </article>
  );
}
