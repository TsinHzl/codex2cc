import assert from "node:assert/strict";
import test from "node:test";
import {
  resolveDocsTarget,
  searchDocEntries,
} from "../pages/docs/docsNavigation.ts";

const entries = [
  {
    id: "qs-tools",
    section: "quick-start",
    title: "客户端配置",
    summary: "Codex CLI Claude Code",
  },
  {
    id: "api-images-gen",
    section: "model-api",
    title: "生成图片 /v1/images/generations",
    summary: "GPT Image 2.5 quality xhigh",
    method: "POST",
  },
  {
    id: "authentication",
    section: "development",
    title: "地址与认证",
    summary: "API Key / Base URL",
  },
  {
    id: "client-mapping",
    section: "admin-api",
    title: "模型映射",
    summary: "Claude Codex",
  },
];

test("existing client anchors retain the correct client after moving sections", () => {
  assert.deepEqual(resolveDocsTarget("#client-claude", entries), {
    id: "qs-tools",
    section: "quick-start",
    client: "claude-code",
  });
  assert.equal(resolveDocsTarget("#client-config", entries).id, "qs-tools");
  assert.equal(resolveDocsTarget("#client-codex", entries).client, "codex-cli");
  assert.equal(
    resolveDocsTarget("#authentication", entries).section,
    "development",
  );
  assert.equal(
    resolveDocsTarget("#client-mapping", entries).section,
    "admin-api",
  );
});

test("deep links resolve before their section is mounted; malformed hashes recover", () => {
  assert.equal(
    resolveDocsTarget("#api-images-gen", entries).section,
    "model-api",
  );
  assert.equal(
    resolveDocsTarget("#%61uthentication", entries).id,
    "authentication",
  );
  for (const hash of ["", "#missing", "#%E0%A4%A"]) {
    assert.equal(resolveDocsTarget(hash, entries).section, "quick-start");
  }
});

test("search combines path, method and parameter terms across all sections", () => {
  assert.equal(
    searchDocEntries("  POST  QUALITY ", entries)[0].id,
    "api-images-gen",
  );
  assert.equal(searchDocEntries("/v1/images", entries)[0].id, "api-images-gen");
  assert.equal(searchDocEntries("claude", entries).length, 2);
  assert.deepEqual(searchDocEntries("  ", entries), []);
  assert.deepEqual(searchDocEntries("POST unknown", entries), []);
});
