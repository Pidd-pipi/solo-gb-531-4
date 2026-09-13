<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { AlertTriangle, Ban, CheckCheck, FileCheck2, Pencil, Play, Plus, RefreshCw, Send, Undo2 } from 'lucide-vue-next'
import AppShell from '../components/common/AppShell.vue'
import PageHeader from '../components/common/PageHeader.vue'
import EvidenceDrawer from '../components/common/EvidenceDrawer.vue'
import { useAuth } from '../hooks/useAuth'
import { useRectificationStore } from '../stores/rectification'
import { useDeviationScenarioStore } from '../stores/deviation-scenario'
import { useCoverageEvaluationStore } from '../stores/coverage-evaluation'
import { useSafeguardStore } from '../stores/safeguard'
import { errorMessage } from '../api/client'
import { groupByState } from '../utils/rectification'
import { rectificationStates, rectificationStateLabels } from '../types/enums/rectification-state'
import type { GenerateRectificationResult, RectificationItem } from '../types/rectification'
import type { Safeguard } from '../types/safeguard'

const store = useRectificationStore()
const scenarios = useDeviationScenarioStore()
const evaluations = useCoverageEvaluationStore()
const safeguards = useSafeguardStore()
const { canEdit, canReview } = useAuth()

const filterScenario = ref<number>()
const generateDialog = ref(false)
const editDialog = ref(false)
const completeDialog = ref(false)
const drawer = ref(false)
const saving = ref(false)
const evidenceTarget = ref<RectificationItem>()
const completeTarget = ref<RectificationItem>()
const generateResult = ref<GenerateRectificationResult>()
const generateForm = reactive<{ evaluation_id?: number; owner_name: string; due_date?: string }>({ evaluation_id: undefined, owner_name: '', due_date: undefined })
const editForm = reactive<{ id: number; owner_name: string; due_date?: string; evidence_note: string }>({ id: 0, owner_name: '', due_date: undefined, evidence_note: '' })
const completeSelection = ref<number[]>([])

const visible = computed(() => filterScenario.value ? store.items.filter((x) => x.scenario_id === filterScenario.value) : store.items)
const groups = computed(() => groupByState(visible.value))
const scenarioLabel = (id: number) => { const item = scenarios.items.find((x) => x.id === id); return item ? `#${id} ${item.guideword.toUpperCase()} ${item.parameter}` : `#${id}` }
const eligibleEvaluations = computed(() => evaluations.items.filter((x) => ['completed', 'confirmed'].includes(x.evaluation_state) && (x.uncovered_paths?.length ?? 0) > 0))
const evaluationLabel = (id?: number) => { const item = evaluations.items.find((x) => x.id === id); return item ? `评估 #${item.id} · ${scenarioLabel(item.scenario_id)} · ${item.uncovered_paths.length} 条缺口` : '选择已完成的评估' }

const boundSafeguardIds = computed(() => new Set(store.items.flatMap((x) => (x.bindings ?? []).map((b) => b.safeguard_id))))
function eligibleSafeguards(item: RectificationItem): Safeguard[] {
  const createdAt = new Date(item.created_at).getTime()
  return safeguards.items.filter((x) => x.target_scenario_id === item.scenario_id
    && x.lifecycle_state === 'active' && !x.verification_expired
    && x.created_at && new Date(x.created_at).getTime() > createdAt
    && !boundSafeguardIds.value.has(x.id))
}
function fmtDate(value?: string | null) { return value ? new Date(value).toLocaleDateString('zh-CN') : '未设置' }

async function refresh() { try { await Promise.all([store.load(filterScenario.value), scenarios.load(), evaluations.load(), safeguards.load()]) } catch (error) { ElMessage.error(errorMessage(error)) } }
async function applyFilter() { try { await store.load(filterScenario.value) } catch (error) { ElMessage.error(errorMessage(error)) } }

function openGenerate() { generateResult.value = undefined; generateForm.evaluation_id = eligibleEvaluations.value[0]?.id; generateForm.owner_name = ''; generateForm.due_date = undefined; generateDialog.value = true }
async function submitGenerate() {
  if (!generateForm.evaluation_id) return ElMessage.warning('请选择要生成的覆盖评估')
  saving.value = true
  try {
    const result = await store.generate({ evaluation_id: generateForm.evaluation_id, owner_name: generateForm.owner_name || undefined, due_date: generateForm.due_date || undefined })
    generateResult.value = result
    if (result.created.length && !result.skipped.length) { ElMessage.success(`已生成 ${result.created.length} 项整改项`); generateDialog.value = false }
    else if (!result.created.length) ElMessage.warning('缺口均已存在整改项，未重复生成')
    else ElMessage.success(`已生成 ${result.created.length} 项，${result.skipped.length} 条缺口因已存在被拦截`)
  } catch (error) { ElMessage.error(errorMessage(error)) } finally { saving.value = false }
}

function openEdit(item: RectificationItem) { editForm.id = item.id; editForm.owner_name = item.owner_name; editForm.due_date = item.due_date ?? undefined; editForm.evidence_note = item.evidence_note; editDialog.value = true }
async function saveEdit() {
  saving.value = true
  try {
    await store.update(editForm.id, { owner_name: editForm.owner_name, evidence_note: editForm.evidence_note, ...(editForm.due_date ? { due_date: editForm.due_date } : {}) })
    editDialog.value = false
    ElMessage.success('整改项已更新')
  } catch (error) { ElMessage.error(errorMessage(error)) } finally { saving.value = false }
}

async function transition(item: RectificationItem, toState: string, message: string) {
  try { await store.transition(item.id, toState); ElMessage.success(message) } catch (error) { ElMessage.error(errorMessage(error)) }
}
async function voidItem(item: RectificationItem) {
  try {
    const { value } = await ElMessageBox.prompt('记录作废原因（至少 3 个字符），作废后保留历史但不再跟进。', `作废整改项 #${item.id}`, { confirmButtonText: '确认作废', cancelButtonText: '取消', inputPattern: /^.{3,}$/, inputErrorMessage: '原因至少 3 个字符' })
    await store.transition(item.id, 'voided', value)
    ElMessage.success('整改项已作废')
  } catch (error) { if (error !== 'cancel') ElMessage.error(errorMessage(error)) }
}
async function returnItem(item: RectificationItem) {
  try {
    const { value } = await ElMessageBox.prompt('记录退回原因，整改项将回到整改中。', `退回整改项 #${item.id}`, { confirmButtonText: '退回', cancelButtonText: '取消', inputPattern: /^.{3,}$/, inputErrorMessage: '原因至少 3 个字符' })
    await store.sendBack(item.id, value)
    ElMessage.success('已退回整改')
  } catch (error) { if (error !== 'cancel') ElMessage.error(errorMessage(error)) }
}

function openComplete(item: RectificationItem) { completeTarget.value = item; completeSelection.value = []; completeDialog.value = true }
async function submitComplete() {
  if (!completeTarget.value) return
  if (!completeSelection.value.length) return ElMessage.warning('完成整改必须绑定至少一条新补录且有效的保护层')
  saving.value = true
  try {
    await store.complete(completeTarget.value.id, completeSelection.value)
    completeDialog.value = false
    ElMessage.success('整改项已完成复核')
  } catch (error) { ElMessage.error(errorMessage(error)) } finally { saving.value = false }
}

function showEvidence(item: RectificationItem) { evidenceTarget.value = item; drawer.value = true }
onMounted(refresh)
</script>

<template>
  <AppShell>
    <PageHeader eyebrow="RECTIFICATION LEDGER" title="整改台账" description="把覆盖推演的未保护缺口转为可跟进整改项；完成复核必须绑定新补录且有效的保护层。">
      <el-button :loading="store.loading" @click="refresh"><RefreshCw :size="16" />刷新</el-button>
      <el-button v-if="canEdit" type="primary" @click="openGenerate"><Plus :size="16" />从评估生成整改项</el-button>
    </PageHeader>
    <section class="rectification-metrics" aria-label="整改统计">
      <div class="incomplete"><span>未完成整改</span><strong>{{ store.summary.incomplete }}</strong></div>
      <div><span>待处理</span><strong>{{ store.summary.by_state.pending ?? 0 }}</strong></div>
      <div><span>整改中</span><strong>{{ store.summary.by_state.in_progress ?? 0 }}</strong></div>
      <div><span>待复核</span><strong>{{ store.summary.by_state.pending_review ?? 0 }}</strong></div>
      <div><span>已完成</span><strong>{{ store.summary.by_state.completed ?? 0 }}</strong></div>
      <div><span>已作废</span><strong>{{ store.summary.by_state.voided ?? 0 }}</strong></div>
    </section>
    <section class="filter-bar">
      <div><el-select v-model="filterScenario" placeholder="全部偏差场景" clearable @change="applyFilter"><el-option v-for="item in scenarios.items" :key="item.id" :label="scenarioLabel(item.id)" :value="item.id" /></el-select></div>
      <span>{{ visible.length }} 项整改项 · 按状态分组</span>
    </section>
    <section v-for="state in rectificationStates" :key="state" class="rectification-group">
      <div class="section-heading">
        <div><p class="eyebrow">{{ state.toUpperCase().replace('_', ' ') }}</p><h2><span class="state-label" :class="state">{{ rectificationStateLabels[state] }}</span></h2></div>
        <span>{{ groups[state].length }} 项</span>
      </div>
      <div v-if="groups[state].length" class="rectification-cards">
        <article v-for="item in groups[state]" :key="item.id" class="rectification-card" :class="{ overdue: item.overdue }">
          <header>
            <span class="scenario-index">#{{ item.id }} · {{ item.node_code }}</span>
            <span class="scenario-tag">{{ scenarioLabel(item.scenario_id) }}</span>
          </header>
          <div class="gap-flow">
            <div><small>原因</small>{{ item.cause }}</div>
            <div class="consequence"><small>后果</small>{{ item.consequence }}</div>
          </div>
          <div class="rectification-meta">
            <span>责任人 <strong :class="{ 'unassigned': !item.owner_name }">{{ item.owner_name || '未指派' }}</strong></span>
            <span>截止 <strong :class="{ 'overdue-text': item.overdue }">{{ fmtDate(item.due_date) }}</strong><AlertTriangle v-if="item.overdue" :size="13" class="overdue-icon" aria-label="已逾期" /></span>
            <span>来源评估 #{{ item.evaluation_id }}</span>
          </div>
          <p v-if="item.evidence_note" class="evidence-line">{{ item.evidence_note }}</p>
          <div v-if="item.bindings?.length" class="binding-line">
            <strong>已绑定保护层</strong>
            <span v-for="binding in item.bindings" :key="binding.safeguard_id">#{{ binding.safeguard_id }} {{ binding.safeguard_name }} · {{ binding.bound_by_name }}</span>
          </div>
          <p v-if="item.state === 'voided' && item.void_reason" class="void-line">作废原因：{{ item.void_reason }}</p>
          <div class="rectification-actions">
            <el-tooltip content="查看证据快照"><el-button circle text aria-label="查看证据" @click="showEvidence(item)"><FileCheck2 :size="15" /></el-button></el-tooltip>
            <template v-if="canEdit && ['pending', 'in_progress', 'pending_review'].includes(item.state)">
              <el-button text aria-label="编辑责任人与截止日期" @click="openEdit(item)"><Pencil :size="14" />编辑</el-button>
            </template>
            <el-button v-if="canEdit && item.state === 'pending'" text type="primary" @click="transition(item, 'in_progress', '已开始整改')"><Play :size="14" />开始整改</el-button>
            <el-button v-if="canEdit && item.state === 'in_progress'" text type="primary" @click="transition(item, 'pending_review', '已提交复核')"><Send :size="14" />提交复核</el-button>
            <el-button v-if="canReview && item.state === 'pending_review'" text type="success" @click="openComplete(item)"><CheckCheck :size="14" />完成复核</el-button>
            <el-button v-if="canReview && item.state === 'pending_review'" text type="warning" @click="returnItem(item)"><Undo2 :size="14" />退回</el-button>
            <el-button v-if="canEdit && ['pending', 'in_progress', 'pending_review'].includes(item.state)" text type="danger" @click="voidItem(item)"><Ban :size="14" />作废</el-button>
          </div>
        </article>
      </div>
      <div v-else class="empty-inline">暂无{{ rectificationStateLabels[state] }}整改项</div>
    </section>

    <el-dialog v-model="generateDialog" title="从覆盖评估生成整改项" width="min(640px, 94vw)">
      <el-form label-position="top" @submit.prevent="submitGenerate">
        <el-form-item label="覆盖评估（已完成/已确认且存在未覆盖路径）" required>
          <el-select v-model="generateForm.evaluation_id" :placeholder="evaluationLabel()">
            <el-option v-for="item in eligibleEvaluations" :key="item.id" :label="evaluationLabel(item.id)" :value="item.id" />
          </el-select>
        </el-form-item>
        <div class="form-grid two">
          <el-form-item label="默认责任人（可稍后逐项指派）"><el-input v-model="generateForm.owner_name" placeholder="留空则未指派" /></el-form-item>
          <el-form-item label="默认截止日期"><el-date-picker v-model="generateForm.due_date" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" /></el-form-item>
        </div>
      </el-form>
      <el-alert v-if="!eligibleEvaluations.length" type="info" :closable="false" title="暂无可用评估：请先在覆盖推演页运行评估并确认存在未覆盖路径。" />
      <div v-if="generateResult" class="generate-result">
        <el-alert v-if="generateResult.created.length" type="success" :closable="false" :title="`已生成 ${generateResult.created.length} 项整改项`" />
        <el-alert v-for="item in generateResult.skipped" :key="item.path_id" type="warning" :closable="false" :title="`缺口「${item.cause} → ${item.consequence}」：${item.reason}`" />
      </div>
      <template #footer>
        <el-button @click="generateDialog = false">关闭</el-button>
        <el-button type="primary" :loading="saving" :disabled="!eligibleEvaluations.length" @click="submitGenerate">生成整改项</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editDialog" :title="`编辑整改项 #${editForm.id}`" width="min(560px, 94vw)">
      <el-form label-position="top" @submit.prevent="saveEdit">
        <div class="form-grid two">
          <el-form-item label="责任人"><el-input v-model="editForm.owner_name" placeholder="未指派" /></el-form-item>
          <el-form-item label="截止日期"><el-date-picker v-model="editForm.due_date" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" /></el-form-item>
        </div>
        <el-form-item label="证据说明"><el-input v-model="editForm.evidence_note" type="textarea" :rows="3" placeholder="记录整改措施、证据位置与说明" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="editDialog = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveEdit">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="completeDialog" :title="`完成复核 · 整改项 #${completeTarget?.id}`" width="min(640px, 94vw)">
      <p class="complete-hint">完成整改必须绑定至少一条<strong>新补录且当前有效</strong>的保护层（属于场景 {{ completeTarget ? scenarioLabel(completeTarget.scenario_id) : '' }}，且登记时间晚于整改项创建时间）。</p>
      <template v-if="completeTarget">
        <el-checkbox-group v-if="eligibleSafeguards(completeTarget).length" v-model="completeSelection">
          <label v-for="option in eligibleSafeguards(completeTarget)" :key="option.id" class="safeguard-option">
            <el-checkbox :value="option.id">
              <strong>#{{ option.id }} {{ option.name }}</strong>
              <span>独立键 {{ option.independence_key }} · 有效性 {{ Math.round(option.effectiveness * 100) }}% · 登记于 {{ fmtDate(option.created_at) }}</span>
            </el-checkbox>
          </label>
        </el-checkbox-group>
        <el-alert v-else type="warning" :closable="false" title="没有可用的新补录有效保护层" description="请先在保护层台账为该场景登记新保护层并完成验证，再回到本页完成复核。" />
      </template>
      <template #footer>
        <el-button @click="completeDialog = false">取消</el-button>
        <el-button type="success" :loading="saving" :disabled="!completeSelection.length" @click="submitComplete">绑定并完成</el-button>
      </template>
    </el-dialog>

    <EvidenceDrawer v-model="drawer" :title="evidenceTarget ? `整改项 #${evidenceTarget.id}` : ''" :note="evidenceTarget?.evidence_note" :evidence="evidenceTarget" />
  </AppShell>
</template>
