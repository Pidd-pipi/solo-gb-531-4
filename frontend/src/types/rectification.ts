import type { RectificationState } from './enums/rectification-state'

export interface RectificationBinding {
  safeguard_id: number
  safeguard_name: string
  bound_by: number
  bound_by_name: string
  bound_at: string
}

export interface RectificationItem {
  id: number
  gap_fingerprint: string
  evaluation_id: number
  scenario_id: number
  scenario_label: string
  path_id: string
  node_code: string
  cause: string
  consequence: string
  owner_name: string
  due_date: string | null
  overdue: boolean
  evidence_note: string
  state: RectificationState
  generated_by: number
  generated_by_name: string
  completed_by?: number
  completed_at?: string
  void_reason?: string
  bindings: RectificationBinding[]
  created_at: string
  updated_at: string
}

export interface RectificationSkipped {
  path_id: string
  cause: string
  consequence: string
  existing_item_id: number
  reason: string
}

export interface GenerateRectificationResult {
  created: RectificationItem[]
  skipped: RectificationSkipped[]
}

export interface RectificationSummary {
  by_state: Record<string, number>
  incomplete: number
  total: number
}

export interface RectificationUpdateInput {
  owner_name?: string
  due_date?: string
  evidence_note?: string
}
