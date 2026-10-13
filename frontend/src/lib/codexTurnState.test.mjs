import assert from "node:assert/strict";
import test from "node:test";

import {
  CODEX_TURN_STATE_TTL_MS,
  computeCodexTurnStateTtl,
  formatCodexTurnStateCountdown,
  parseCodexTurnStateSetAt,
} from "./codexTurnState.ts";

const SET_AT = "2026-09-17T10:00:00Z";
const SET_AT_MS = Date.parse(SET_AT);

test("parseCodexTurnStateSetAt handles empty and invalid input", () => {
  assert.equal(parseCodexTurnStateSetAt(undefined), null);
  assert.equal(parseCodexTurnStateSetAt(null), null);
  assert.equal(parseCodexTurnStateSetAt(""), null);
  assert.equal(parseCodexTurnStateSetAt("   "), null);
  assert.equal(parseCodexTurnStateSetAt("not-a-date"), null);
  assert.equal(parseCodexTurnStateSetAt(SET_AT), SET_AT_MS);
  assert.equal(parseCodexTurnStateSetAt(" 2026-09-17T10:00:00+08:00 "), Date.parse("2026-09-17T02:00:00Z"));
});

test("computeCodexTurnStateTtl reports unknown when set_at is missing", () => {
  assert.deepEqual(computeCodexTurnStateTtl(undefined, SET_AT_MS), { kind: "unknown" });
  assert.deepEqual(computeCodexTurnStateTtl("garbage", SET_AT_MS), { kind: "unknown" });
});

test("computeCodexTurnStateTtl counts down within the hour", () => {
  const fresh = computeCodexTurnStateTtl(SET_AT, SET_AT_MS);
  assert.equal(fresh.kind, "active");
  assert.equal(fresh.remainingMs, CODEX_TURN_STATE_TTL_MS);
  assert.equal(fresh.ratio, 1);
  assert.equal(fresh.warning, false);

  const halfway = computeCodexTurnStateTtl(SET_AT, SET_AT_MS + 30 * 60 * 1000);
  assert.equal(halfway.kind, "active");
  assert.equal(halfway.remainingMs, 30 * 60 * 1000);
  assert.equal(halfway.ratio, 0.5);
  assert.equal(halfway.warning, false);

  const nearEnd = computeCodexTurnStateTtl(SET_AT, SET_AT_MS + 51 * 60 * 1000);
  assert.equal(nearEnd.kind, "active");
  assert.equal(nearEnd.remainingMs, 9 * 60 * 1000);
  assert.equal(nearEnd.warning, true);
});

test("computeCodexTurnStateTtl clamps a future set_at to the full TTL", () => {
  const skewed = computeCodexTurnStateTtl(SET_AT, SET_AT_MS - 5 * 60 * 1000);
  assert.equal(skewed.kind, "active");
  assert.equal(skewed.remainingMs, CODEX_TURN_STATE_TTL_MS);
  assert.equal(skewed.ratio, 1);
});

test("computeCodexTurnStateTtl reports expired at and after the hour", () => {
  assert.deepEqual(computeCodexTurnStateTtl(SET_AT, SET_AT_MS + CODEX_TURN_STATE_TTL_MS), {
    kind: "expired",
    remainingMs: 0,
  });
  assert.deepEqual(computeCodexTurnStateTtl(SET_AT, SET_AT_MS + 2 * CODEX_TURN_STATE_TTL_MS), {
    kind: "expired",
    remainingMs: 0,
  });
});

test("formatCodexTurnStateCountdown renders mm:ss with ceil to the second", () => {
  assert.equal(formatCodexTurnStateCountdown(CODEX_TURN_STATE_TTL_MS), "60:00");
  assert.equal(formatCodexTurnStateCountdown(59 * 60 * 1000 + 59_500), "60:00");
  assert.equal(formatCodexTurnStateCountdown(9 * 60 * 1000 + 5_000), "09:05");
  assert.equal(formatCodexTurnStateCountdown(999), "00:01");
  assert.equal(formatCodexTurnStateCountdown(0), "00:00");
  assert.equal(formatCodexTurnStateCountdown(-5_000), "00:00");
});
