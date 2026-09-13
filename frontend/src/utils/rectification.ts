import { rectificationStates, type RectificationState } from '../types/enums/rectification-state'
import type { RectificationItem } from '../types/rectification'

export function groupByState(items: RectificationItem[]): Record<RectificationState, RectificationItem[]> {
  const groups = Object.fromEntries(rectificationStates.map((state) => [state, [] as RectificationItem[]])) as Record<RectificationState, RectificationItem[]>
  for (const item of items) {
    const bucket = groups[item.state]
    if (bucket) bucket.push(item)
  }
  return groups
}

export function incompleteCount(items: RectificationItem[]): number {
  return items.filter((item) => item.state === 'pending' || item.state === 'in_progress' || item.state === 'pending_review').length
}
