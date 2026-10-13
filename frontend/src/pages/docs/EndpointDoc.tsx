import { memo, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Copy,
  Check,
  Play,
  Loader2,
  ChevronRight,
  Link as LinkIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import type { EndpointSpec } from "./docsContent";
import { useHighlightedHtml } from "../../hooks/useHighlighter";

export const CodeBlock = memo(function CodeBlock({
  label,
  content,
  lang,
}: {
  label?: string;
  content: string;
  lang?: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const [shouldHighlight, setShouldHighlight] = useState(false);
  const blockRef = useRef<HTMLDivElement>(null);
  const resolvedLang =
    lang ||
    (label?.endsWith(".toml")
      ? "toml"
      : label?.endsWith(".json")
        ? "json"
        : "bash");
  const highlightedHtml = useHighlightedHtml(
    shouldHighlight ? content : "",
    resolvedLang,
  );

  useEffect(() => {
    const el = blockRef.current;
    if (!el || shouldHighlight) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setShouldHighlight(true);
          observer.disconnect();
        }
      },
      { rootMargin: "360px 0px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [shouldHighlight]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(content);
    } catch {
      const ta = document.createElement("textarea");
      ta.value = content;
      ta.style.cssText = "position:fixed;left:-9999px";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div ref={blockRef} className="code-panel relative">
      {label && (
        <div className="code-panel-header">
          <span className="code-panel-label">{label}</span>
          <button
            onClick={() => void handleCopy()}
            className={`code-panel-copy ${
              copied ? "bg-emerald-500/20 text-emerald-300" : ""
            }`}
            aria-label={copied ? t("common.copied") : t("common.copy")}
          >
            {copied ? (
              <Check className="size-3.5" />
            ) : (
              <Copy className="size-3.5" />
            )}
          </button>
        </div>
      )}
      {!label && (
        <div className="absolute top-2 right-2 z-10">
          <button
            onClick={() => void handleCopy()}
            className={`code-panel-copy ${copied ? "bg-emerald-500/20 text-emerald-300" : ""}`}
            aria-label={copied ? t("common.copied") : t("common.copy")}
          >
            {copied ? (
              <Check className="size-3" />
            ) : (
              <Copy className="size-3" />
            )}
          </button>
        </div>
      )}
      {highlightedHtml ? (
        <div
          className={`code-panel-pre shiki-wrapper ${lang === "json" ? "text-[13px]" : "text-sm"}`}
          dangerouslySetInnerHTML={{ __html: highlightedHtml }}
        />
      ) : (
        <pre
          className={`code-panel-pre ${lang === "json" ? "text-[13px]" : "text-sm"}`}
        >
          <code>{content}</code>
        </pre>
      )}
    </div>
  );
});

export function MethodBadge({ method, sm }: { method: string; sm?: boolean }) {
  const colors: Record<string, string> = {
    GET: "bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400 border-emerald-200 dark:border-emerald-800",
    POST: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400 border-blue-200 dark:border-blue-800",
    PUT: "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400 border-amber-200 dark:border-amber-800",
    DELETE:
      "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400 border-red-200 dark:border-red-800",
  };
  const size = sm
    ? "px-1.5 py-0.5 rounded text-[10px]"
    : "px-2.5 py-1 rounded-lg text-xs";
  return (
    <span
      className={`inline-flex items-center font-bold border ${size} ${colors[method] || "bg-muted text-foreground border-border"}`}
    >
      {method}
    </span>
  );
}

function TryItDialog({
  open,
  onClose,
  method,
  path,
  defaultBody,
  apiKey,
  baseUrl,
  allKeys,
}: {
  open: boolean;
  onClose: () => void;
  method: string;
  path: string;
  defaultBody: string;
  apiKey: string;
  baseUrl: string;
  allKeys: { name: string; key: string }[];
}) {
  const { t } = useTranslation();
  const [body, setBody] = useState(defaultBody);
  const [token, setToken] = useState(apiKey);
  const [response, setResponse] = useState("");
  const [status, setStatus] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [duration, setDuration] = useState<number | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  useEffect(() => () => abortRef.current?.abort(), []);

  useEffect(() => {
    if (open) {
      setBody(defaultBody);
      setToken(apiKey);
      setResponse("");
      setStatus(null);
      setDuration(null);
    }
  }, [open, defaultBody, apiKey]);

  const handleSend = async () => {
    if (loading) return;
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    setResponse("");
    setStatus(null);
    setDuration(null);
    const start = performance.now();
    try {
      const isAdmin = path.startsWith("/api/admin");
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
      };
      if (isAdmin) {
        headers["X-Admin-Key"] = token;
      } else if (path.startsWith("/v1/messages")) {
        headers["x-api-key"] = token;
        headers["anthropic-version"] = "2023-06-01";
      } else if (path !== "/health") {
        headers["Authorization"] = `Bearer ${token}`;
      }

      const isGet = method === "GET";
      const url = baseUrl + path;
      const res = await fetch(url, {
        method,
        headers,
        signal: controller.signal,
        body: isGet ? undefined : body.trim() || undefined,
      });
      setStatus(res.status);
      setDuration(Math.round(performance.now() - start));
      const text = await res.text();
      try {
        setResponse(JSON.stringify(JSON.parse(text), null, 2));
      } catch {
        setResponse(text);
      }
    } catch (e) {
      setDuration(Math.round(performance.now() - start));
      setResponse(`Error: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setLoading(false);
    }
  };

  const statusColor =
    status === null
      ? ""
      : status < 300
        ? "text-emerald-600"
        : status < 400
          ? "text-amber-600"
          : "text-red-500";
  const statusBg =
    status === null
      ? ""
      : status < 300
        ? "bg-emerald-50 dark:bg-emerald-900/20"
        : status < 400
          ? "bg-amber-50 dark:bg-amber-900/20"
          : "bg-red-50 dark:bg-red-900/20";

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) {
          abortRef.current?.abort();
          onClose();
        }
      }}
    >
      <DialogContent className="sm:max-w-4xl max-h-[90dvh] overflow-hidden flex flex-col gap-0 p-0">
        <DialogTitle className="sr-only">
          {t("apiRef.tryIt.button")} {path}
        </DialogTitle>
        <DialogDescription className="sr-only">
          {t("apiRef.tryIt.requestBody")} / {t("apiRef.tryIt.responseTitle")}
        </DialogDescription>
        <div className="flex flex-wrap items-center gap-3 pl-4 pr-12 py-4 border-b border-border bg-muted/30">
          <div className="flex min-w-0 items-center gap-2.5 flex-1 basis-48 px-3 py-2 rounded-xl border border-border bg-background">
            <MethodBadge method={method} />
            <code className="min-w-0 break-all text-xs font-mono">{path}</code>
          </div>
          <Button
            onClick={() => void handleSend()}
            disabled={loading}
            className="gap-2 bg-emerald-600 px-5 text-white shrink-0 hover:bg-emerald-600/90 dark:bg-emerald-500/90 dark:hover:bg-emerald-500"
          >
            {loading ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Play className="size-4" />
            )}
            {loading ? t("apiRef.tryIt.sending") : t("apiRef.tryIt.send")}
          </Button>
        </div>

        <div className="grid md:grid-cols-2 flex-1 min-h-0 overflow-y-auto">
          <div className="min-w-0 p-4 space-y-4 md:border-r border-b md:border-b-0 border-border">
            {path !== "/health" && (
              <div className="rounded-xl border border-border overflow-visible">
                <div className="px-4 py-2.5 bg-muted/30 border-b border-border">
                  <span className="text-sm font-semibold text-foreground">
                    {t("apiRef.tryIt.authTitle")}
                  </span>
                </div>
                <div className="p-4 space-y-3">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">
                          {path.startsWith("/v1/messages")
                            ? "x-api-key"
                            : path.startsWith("/api/admin")
                              ? "X-Admin-Key"
                              : "Authorization"}
                        </span>
                        <span className="code-inline text-[11px]">string</span>
                      </div>
                      <Badge
                        variant="destructive"
                        className="mt-1 text-[10px] px-1.5 py-0"
                      >
                        {t("apiRef.tryIt.required")}
                      </Badge>
                    </div>
                    <input
                      type="password"
                      autoComplete="off"
                      data-1p-ignore="true"
                      data-lpignore="true"
                      aria-label={t("apiRef.tryIt.keyPlaceholder")}
                      className="w-full min-w-0 px-3 py-1.5 rounded-lg border border-border bg-background text-sm font-medium focus:outline-none focus:ring-2 focus:ring-primary/30"
                      placeholder={t("apiRef.tryIt.keyPlaceholder")}
                      value={token}
                      onChange={(e) => setToken(e.target.value)}
                    />
                  </div>
                  {allKeys.length > 0 && (
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-xs text-muted-foreground shrink-0">
                        {t("apiRef.tryIt.selectKey")}
                      </span>
                      <Select
                        value={token}
                        onValueChange={(v) => setToken(v)}
                        options={allKeys.map((k) => ({
                          label: `${k.name} — ${k.key.length > 20 ? k.key.slice(0, 8) + "..." + k.key.slice(-4) : k.key}`,
                          value: k.key,
                        }))}
                      />
                    </div>
                  )}
                </div>
              </div>
            )}

            {method !== "GET" && method !== "DELETE" && (
              <div className="rounded-xl border border-border overflow-hidden">
                <div className="px-4 py-2.5 bg-muted/30 border-b border-border">
                  <span className="text-sm font-semibold text-foreground">
                    {t("apiRef.tryIt.requestBody")}
                  </span>
                </div>
                <textarea
                  aria-label={t("apiRef.tryIt.requestBody")}
                  className="w-full min-w-0 h-56 resize-y border-0 bg-background p-4 text-[15px] leading-relaxed outline-none"
                  style={{ fontFamily: "var(--font-mono)" }}
                  value={body}
                  onChange={(e) => setBody(e.target.value)}
                  spellCheck={false}
                />
              </div>
            )}
          </div>

          <div className="min-w-0 p-4">
            <div className="rounded-xl border border-border overflow-hidden h-full flex flex-col">
              <div className="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center justify-between">
                <span className="text-sm font-semibold text-foreground">
                  {t("apiRef.tryIt.responseTitle")}
                </span>
                {status !== null && (
                  <div className="flex items-center gap-2.5">
                    <span
                      className={`px-2 py-0.5 rounded-md text-xs font-bold ${statusColor} ${statusBg}`}
                    >
                      {status}
                    </span>
                    {duration !== null && (
                      <span className="text-xs text-muted-foreground">
                        {duration}ms
                      </span>
                    )}
                  </div>
                )}
              </div>
              <div className="flex-1 overflow-auto">
                {response ? (
                  <pre
                    className="p-4 text-[13px] text-foreground leading-relaxed whitespace-pre-wrap break-all max-h-[480px]"
                    style={{ fontFamily: "var(--font-mono)" }}
                  >
                    <code>{response}</code>
                  </pre>
                ) : (
                  <div className="flex items-center justify-center h-full min-h-[200px] text-sm text-muted-foreground">
                    {loading ? (
                      <div className="flex items-center gap-2">
                        <Loader2 className="size-4 animate-spin" />
                        <span>{t("apiRef.tryIt.sending")}</span>
                      </div>
                    ) : (
                      <span>{t("apiRef.tryIt.placeholder")}</span>
                    )}
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

export const EndpointDoc = memo(function EndpointDoc({
  endpoint,
  apiKey = "",
  baseUrl = "",
  allKeys = [],
  activeId,
}: {
  endpoint: EndpointSpec;
  apiKey?: string;
  baseUrl?: string;
  allKeys?: { name: string; key: string }[];
  activeId: string;
}) {
  const { t, i18n } = useTranslation();
  const zh = i18n.language.startsWith("zh");
  const { id, method, path, title, description, responses, parameters } =
    endpoint;
  const [expanded, setExpanded] = useState(activeId === id);
  const [activeResponse, setActiveResponse] = useState(0);
  const [activeRequest, setActiveRequest] = useState(0);
  const [tryOpen, setTryOpen] = useState(false);
  useEffect(() => {
    if (activeId === id) setExpanded(true);
  }, [activeId, id]);
  const requests = [
    {
      label: endpoint.transport === "websocket" ? "WebSocket" : "cURL",
      lang: "bash",
      content: endpoint.curl,
    },
    ...(endpoint.requestExamples || []),
  ];
  const supportsTryIt =
    !endpoint.transport &&
    !path.includes(":") &&
    path !== "/api/admin/accounts/import";
  const response = responses[activeResponse] || responses[0];
  return (
    <article id={id} className="docs-endpoint">
      <div className="docs-endpoint-heading">
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          aria-expanded={expanded}
          aria-controls={`${id}-body`}
        >
          <ChevronRight
            className={`size-4 shrink-0 text-muted-foreground ${expanded ? "rotate-90" : ""}`}
          />
          <MethodBadge method={method} sm />
          <span className="min-w-0">
            <strong>{title}</strong>
            <span className="docs-endpoint-path">{path}</span>
          </span>
        </button>
        <a
          href={`#${id}`}
          aria-label={`${zh ? "章节链接" : "Section link"}: ${title}`}
          title={zh ? "章节链接" : "Section link"}
          className="p-2 rounded-md text-muted-foreground hover:text-primary"
        >
          <LinkIcon className="size-3.5" />
        </a>
      </div>
      {expanded && (
        <div id={`${id}-body`} className="docs-endpoint-body">
          <p className="docs-endpoint-description">{description}</p>
          {!!parameters?.length && (
            <>
              <h4 className="docs-endpoint-subtitle">
                {zh ? "请求参数" : "Request parameters"}
              </h4>
              <div className="docs-table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th scope="col">
                        {zh ? "参数 / 类型" : "Parameter / type"}
                      </th>
                      <th scope="col">{zh ? "说明" : "Description"}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {parameters.map((p) => (
                      <tr key={p.name}>
                        <td>
                          <code>{p.name}</code>
                          <div className="text-xs text-muted-foreground">
                            {p.type} ·{" "}
                            {p.required
                              ? zh
                                ? "必填"
                                : "required"
                              : zh
                                ? "可选"
                                : "optional"}
                          </div>
                        </td>
                        <td>
                          {p.description}
                          {p.defaultValue && (
                            <div className="text-xs text-muted-foreground">
                              {zh ? "默认" : "Default"}:{" "}
                              <code>{p.defaultValue}</code>
                            </div>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
          <div className="flex items-center justify-between gap-3 mb-3">
            <h4 className="docs-endpoint-subtitle">
              {zh ? "请求示例" : "Request example"}
            </h4>
            {supportsTryIt && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setTryOpen(true)}
              >
                <Play className="size-3.5" />
                {t("apiRef.tryIt.button")}
              </Button>
            )}
          </div>
          {requests.length > 1 && (
            <div
              className="docs-example-tabs"
              aria-label={zh ? "请求示例" : "Request examples"}
            >
              {requests.map((request, i) => (
                <button
                  type="button"
                  key={request.label}
                  aria-pressed={i === activeRequest}
                  onClick={() => setActiveRequest(i)}
                >
                  {request.label}
                </button>
              ))}
            </div>
          )}
          <CodeBlock {...(requests[activeRequest] || requests[0])} />
          <h4 className="docs-endpoint-subtitle">
            {zh ? "响应示例" : "Response examples"}
          </h4>
          <div
            className="docs-example-tabs"
            aria-label={zh ? "响应示例" : "Response examples"}
          >
            {responses.map((r, i) => (
              <button
                type="button"
                key={`${r.code}-${i}`}
                aria-pressed={i === activeResponse}
                onClick={() => setActiveResponse(i)}
              >
                {r.code}
                {r.label ? ` · ${r.label}` : ""}
              </button>
            ))}
          </div>
          {response && (
            <CodeBlock
              label={`${response.code}${response.label ? ` · ${response.label}` : ""}`}
              content={response.body}
              lang={response.lang || "json"}
            />
          )}
        </div>
      )}
      {tryOpen && (
        <TryItDialog
          open={tryOpen}
          onClose={() => setTryOpen(false)}
          method={method}
          path={path}
          defaultBody={endpoint.defaultBody || ""}
          apiKey={apiKey}
          baseUrl={baseUrl}
          allKeys={allKeys}
        />
      )}
    </article>
  );
});
