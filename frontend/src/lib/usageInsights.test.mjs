import assert from 'node:assert/strict'
import test from 'node:test'

import { buildModelShareData, formatSharePercent } from './usageInsights.ts'

function model(model, overrides = {}) {
  return {
    model,
    requests: 1,
    tokens: 100,
    input_tokens: 80,
    output_tokens: 20,
    cached_tokens: 0,
    account_billed: 0,
    user_billed: 0,
    error_count: 0,
    ...overrides,
  }
}

test('model ranking and shares follow the selected metric, retaining free models', () => {
  const stats = [
    model('popular-free', { requests: 80 }),
    model('expensive', { requests: 5, user_billed: 9 }),
    model('standard', { requests: 15, user_billed: 1 }),
  ]
  const amount = buildModelShareData(stats, 'amount', 'Other')
  assert.equal(amount.total, 10)
  assert.deepEqual(amount.items.map(({ model, value, share }) => [model, value, share]), [
    ['expensive', 9, 90],
    ['standard', 1, 10],
    ['popular-free', 0, 0],
  ])

  const requests = buildModelShareData(stats, 'requests', 'Other')
  assert.equal(requests.total, 100)
  assert.deepEqual(requests.items.map(({ model, value, share }) => [model, value, share]), [
    ['popular-free', 80, 80],
    ['standard', 15, 15],
    ['expensive', 5, 5],
  ])
})

test('the five leading models and Other preserve every aggregate field', () => {
  const stats = [1, 2, 3, 4, 5, 6, 7].map((value) => model(`model-${value}`, {
    requests: value * 10,
    tokens: value * 100,
    user_billed: value,
    error_count: value,
  }))
  const { items, total } = buildModelShareData(stats, 'amount', '其他')
  assert.equal(total, 28)
  assert.deepEqual(items.slice(0, 5).map((item) => item.model), [
    'model-7', 'model-6', 'model-5', 'model-4', 'model-3',
  ])
  assert.deepEqual(items[5], {
    key: 'aggregate:other',
    model: '其他',
    requests: 30,
    tokens: 300,
    amount: 3,
    errors: 3,
    value: 3,
    share: (3 / 28) * 100,
    isOther: true,
  })
  assert.ok(Math.abs(items.reduce((sum, item) => sum + item.share, 0) - 100) < 1e-10)
})

test('Other has a distinct key even when a real model has the same label', () => {
  const stats = [
    model('aggregate:other', { user_billed: 10 }),
    ...Array.from({ length: 5 }, (_, index) => model(`model-${index}`)),
  ]
  const { items } = buildModelShareData(stats, 'amount', 'aggregate:other')
  assert.equal(items[0].model, items[5].model)
  assert.notEqual(items[0].key, items[5].key)
  assert.equal(new Set(items.map((item) => item.key)).size, items.length)
  assert.equal(items[0].isOther, false)
  assert.equal(items[5].isOther, true)
})

test('invalid and negative numerical inputs become zero without hiding rows', () => {
  const invalidValues = [NaN, Infinity, -Infinity, -1, undefined, null, '10']
  for (const invalid of invalidValues) {
    const { items, total } = buildModelShareData([model('invalid', {
      requests: invalid,
      tokens: invalid,
      user_billed: invalid,
      error_count: invalid,
    })], 'amount', 'Other')
    assert.equal(items.length, 1)
    assert.equal(total, 0)
    for (const field of ['requests', 'tokens', 'amount', 'errors', 'value', 'share']) {
      assert.equal(items[0][field], 0, `${field} for ${String(invalid)}`)
    }
  }
})

test('empty and all-zero metrics produce no invented share', () => {
  assert.deepEqual(buildModelShareData([], 'requests', 'Other'), { items: [], total: 0 })
  const { items, total } = buildModelShareData([
    model('free-a', { requests: 4 }),
    model('free-b', { requests: 2 }),
  ], 'amount', 'Other')
  assert.equal(total, 0)
  assert.equal(items.length, 2)
  assert.ok(items.every((item) => item.value === 0 && item.share === 0 && !item.isOther))
})

test('ranking leaves the source array and its records unchanged', () => {
  const stats = Object.freeze([
    Object.freeze(model('first', { requests: 1, user_billed: 1 })),
    Object.freeze(model('second', { requests: 8, user_billed: 8 })),
  ])
  const before = structuredClone(stats)
  buildModelShareData(stats, 'amount', 'Other')
  buildModelShareData(stats, 'requests', 'Other')
  assert.deepEqual(stats, before)
})

test('share formatting distinguishes tiny nonzero amounts and incomplete totals', () => {
  const cases = [
    [0, '0%'],
    [-1, '0%'],
    [NaN, '0%'],
    [Infinity, '0%'],
    [0.00001, '<0.1%'],
    [0.09999, '<0.1%'],
    [0.1, '0.1%'],
    [12.34, '12.3%'],
    [99.9, '99.9%'],
    [99.90001, '>99.9%'],
    [99.99999, '>99.9%'],
    [100, '100%'],
    [101, '100%'],
  ]
  for (const [share, expected] of cases) {
    assert.equal(formatSharePercent(share), expected, String(share))
  }
})
