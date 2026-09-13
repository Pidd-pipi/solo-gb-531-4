import { defineStore } from 'pinia'
import { ref } from 'vue'
import * as rectificationApi from '../api/rectification'
import type { GenerateRectificationResult, RectificationItem, RectificationSummary, RectificationUpdateInput } from '../types/rectification'

export const useRectificationStore = defineStore('rectification-items', () => {
  const items = ref<RectificationItem[]>([])
  const summary = ref<RectificationSummary>({ by_state: {}, incomplete: 0, total: 0 })
  const loading = ref(false)
  async function load(scenarioId?: number, state?: string) {
    loading.value = true
    try {
      const [page, stats] = await Promise.all([rectificationApi.listRectificationItems(scenarioId, state), rectificationApi.getRectificationSummary()])
      items.value = page.items
      summary.value = stats
    } finally {
      loading.value = false
    }
  }
  async function generate(input: { evaluation_id: number; owner_name?: string; due_date?: string }): Promise<GenerateRectificationResult> {
    const result = await rectificationApi.generateRectificationItems(input)
    await load()
    return result
  }
  async function update(id: number, input: RectificationUpdateInput) { const item = await rectificationApi.updateRectificationItem(id, input); await load(); return item }
  async function transition(id: number, toState: string, reason?: string) { const item = await rectificationApi.transitionRectificationItem(id, toState, reason); await load(); return item }
  async function complete(id: number, safeguardIds: number[]) { const item = await rectificationApi.completeRectificationItem(id, safeguardIds); await load(); return item }
  async function sendBack(id: number, reason: string) { const item = await rectificationApi.returnRectificationItem(id, reason); await load(); return item }
  return { items, summary, loading, load, generate, update, transition, complete, sendBack }
})
