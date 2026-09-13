import { api, json, query } from './client'
import { normalizePage, type PageData } from '../types/common'
import type {
  GenerateRectificationResult,
  RectificationItem,
  RectificationSummary,
  RectificationUpdateInput,
} from '../types/rectification'

export async function listRectificationItems(scenarioId?: number, state?: string): Promise<PageData<RectificationItem>> {
  return normalizePage(await api<PageData<RectificationItem> | RectificationItem[]>(`/rectification-items${query({ scenario_id: scenarioId, state, page_size: 100 })}`))
}
export const getRectificationSummary = () => api<RectificationSummary>('/rectification-items/summary')
export const getRectificationItem = (id: number) => api<RectificationItem>(`/rectification-items/${id}`)
export const generateRectificationItems = (input: { evaluation_id: number; owner_name?: string; due_date?: string }) =>
  api<GenerateRectificationResult>('/rectification-items/generate', json('POST', input))
export const updateRectificationItem = (id: number, input: RectificationUpdateInput) =>
  api<RectificationItem>(`/rectification-items/${id}`, json('PUT', input))
export const transitionRectificationItem = (id: number, toState: string, reason?: string) =>
  api<RectificationItem>(`/rectification-items/${id}/transition`, json('POST', { to_state: toState, reason }))
export const completeRectificationItem = (id: number, safeguardIds: number[]) =>
  api<RectificationItem>(`/rectification-items/${id}/complete`, json('POST', { safeguard_ids: safeguardIds }))
export const returnRectificationItem = (id: number, reason: string) =>
  api<RectificationItem>(`/rectification-items/${id}/return`, json('POST', { reason }))
