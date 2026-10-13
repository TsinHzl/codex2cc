import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { normalizeVisibleChannels, toggleVisibleChannel } from './visibleChannels.ts'

test('normalizeVisibleChannels mirrors the backend rules', () => {
  assert.deepEqual(normalizeVisibleChannels(null), ['codex', 'relay', 'claude'])
  assert.deepEqual(normalizeVisibleChannels(undefined), ['codex', 'relay', 'claude'])
  assert.deepEqual(normalizeVisibleChannels([]), ['codex'])
  assert.deepEqual(normalizeVisibleChannels(['claude']), ['codex', 'claude'])
  assert.deepEqual(normalizeVisibleChannels([' Claude ', '', 'codex', 'relay', 'openai', 42]), ['codex', 'relay', 'claude'])
  assert.deepEqual(normalizeVisibleChannels('codex'), ['codex', 'relay', 'claude'])
})

test('toggleVisibleChannel never drops the fallback channel', () => {
  assert.deepEqual(toggleVisibleChannel(['codex', 'relay', 'claude'], 'claude'), ['codex', 'relay'])
  assert.deepEqual(toggleVisibleChannel(['codex'], 'openai'), ['codex'])
  assert.deepEqual(toggleVisibleChannel(['codex'], 'codex'), ['codex'])
})

test('dashboard, accounts and the usage channel filter consume the visibility setting', () => {
  const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8')
  assert.match(read('../App.tsx'), /<VisibleChannelsProvider>/)
  assert.match(read('../pages/Dashboard.tsx'), /useVisibleChannels\(\)/)
  assert.match(read('../components/ChannelFilter.tsx'), /useVisibleChannels\(\)/)
})
