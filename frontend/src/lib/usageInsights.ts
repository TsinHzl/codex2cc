import type { UsageModelStat } from '../types'

export type ModelShareMetric = 'amount' | 'requests'

export interface ModelShareItem {
  key: string
  model: string
  requests: number
  tokens: number
  amount: number
  errors: number
  value: number
  share: number
  isOther: boolean
}

function nonNegativeNumber(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : 0
}

export function buildModelShareData(
  stats: UsageModelStat[],
  metric: ModelShareMetric,
  otherLabel: string,
): { items: ModelShareItem[]; total: number } {
  const models = stats.map((item, index): ModelShareItem => {
    const requests = nonNegativeNumber(item.requests)
    const amount = nonNegativeNumber(item.user_billed)
    return {
      key: `model:${index}:${item.model}`,
      model: item.model || 'unknown',
      requests,
      tokens: nonNegativeNumber(item.tokens),
      amount,
      errors: nonNegativeNumber(item.error_count),
      value: metric === 'amount' ? amount : requests,
      share: 0,
      isOther: false,
    }
  }).sort((a, b) => b.value - a.value)

  const total = models.reduce((sum, item) => sum + item.value, 0)
  const items = models.slice(0, 5)
  if (models.length > 5) {
    const other = models.slice(5).reduce<ModelShareItem>((sum, item) => ({
      ...sum,
      requests: sum.requests + item.requests,
      tokens: sum.tokens + item.tokens,
      amount: sum.amount + item.amount,
      errors: sum.errors + item.errors,
      value: sum.value + item.value,
    }), {
      key: 'aggregate:other',
      model: otherLabel,
      requests: 0,
      tokens: 0,
      amount: 0,
      errors: 0,
      value: 0,
      share: 0,
      isOther: true,
    })
    items.push(other)
  }

  return {
    items: items.map((item) => ({
      ...item,
      share: total > 0 ? (item.value / total) * 100 : 0,
    })),
    total,
  }
}

export function formatSharePercent(share: number): string {
  if (!Number.isFinite(share) || share <= 0) return '0%'
  if (share >= 100) return '100%'
  if (share < 0.1) return '<0.1%'
  if (share > 99.9) return '>99.9%'
  return `${share.toFixed(1)}%`
}
