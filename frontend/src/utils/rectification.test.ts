import { describe, expect, it } from 'vitest'
import { groupByState, incompleteCount } from './rectification'
import { rectificationStates } from '../types/enums/rectification-state'
import type { RectificationItem } from '../types/rectification'

function item(id: number, state: RectificationItem['state']): RectificationItem {
  return {
    id,
    gap_fingerprint: `fp-${id}`,
    evaluation_id: 1,
    scenario_id: 1,
    scenario_label: 'MORE pressure',
    path_id: `P-${id}`,
    node_code: 'R-201',
    cause: 'cause',
    consequence: 'consequence',
    owner_name: '',
    due_date: null,
    overdue: false,
    evidence_note: '',
    state,
    generated_by: 1,
    generated_by_name: 'engineer',
    bindings: [],
    created_at: '2026-09-12T00:00:00Z',
    updated_at: '2026-09-12T00:00:00Z',
  }
}

describe('groupByState', () => {
  it('groups every item under its own state and keeps empty buckets', () => {
    const groups = groupByState([item(1, 'pending'), item(2, 'pending'), item(3, 'completed')])
    expect(groups.pending.map((x) => x.id)).toEqual([1, 2])
    expect(groups.completed.map((x) => x.id)).toEqual([3])
    expect(groups.in_progress).toEqual([])
    expect(rectificationStates.every((state) => Array.isArray(groups[state]))).toBe(true)
  })
})

describe('incompleteCount', () => {
  it('counts pending, in_progress and pending_review but not terminal states', () => {
    const items = [item(1, 'pending'), item(2, 'in_progress'), item(3, 'pending_review'), item(4, 'completed'), item(5, 'voided')]
    expect(incompleteCount(items)).toBe(3)
    expect(incompleteCount([])).toBe(0)
  })
})
