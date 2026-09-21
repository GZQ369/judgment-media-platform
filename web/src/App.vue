<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  AlertCircle,
  Archive,
  ArrowLeft,
  Check,
  CheckCircle2,
  ChevronRight,
  CircleAlert,
  CirclePause,
  CirclePlay,
  CircleHelp,
  Clock3,
  ExternalLink,
  FileBox,
  FilePlus2,
  Filter,
  GitBranch,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Send,
  ShieldCheck,
  Square,
  X,
  XCircle,
} from 'lucide-vue-next'
import { authorizePublication, cancelRun, commitPublication, createPublicationCandidate, createRun, getAnalyticsCapabilities, getAnalyticsSummary, getPipelineRun, getPipelineRunEvents, getPipelineRuns, getPublication, getPublicationCapabilities, getPublicationInsights, getRun, getRunEvents, getRuns, getPublications, preparePublication, reconcilePublication, resumeRun, retryRun, syncPublicationInsights } from './api'
import { getRunPublicationAccounts, getRunPublicationVariants, mapAnalyticsCapabilities, mapAnalyticsSummary, mapEvents, mapPipelineRun, mapPipelineRuns, mapPublication, mapPublicationAuthorization, mapPublicationCandidate, mapPublicationCapabilities, mapPublicationInsights, mapPublications, mapRun, mapRuns, publicationStatusLabel, publicationStatusTone } from './mapper'
import { canCancel, canResume, canRetry } from './status'
import type { AnalyticsCapability, AnalyticsSummary, CreateRunInput, PipelineTask, Platform, PublicationCandidate, PublicationCapability, PublicationCandidateRequest, PublicationRecord, RunTask, TaskType } from './types'

const tasks = ref<RunTask[]>([])
const selectedId = ref<string | null>(null)
const selectedDetail = ref<RunTask | null>(null)
const isLoading = ref(false)
const isMutating = ref(false)
const showCreate = ref(false)
const errorMessage = ref('')
const actionMessage = ref('')
const search = ref('')
const statusFilter = ref('all')
const typeFilter = ref('all')
const platformFilter = ref('all')
const activeView = ref<'runs' | 'pipeline' | 'publishing'>('runs')
const createForm = ref<CreateRunInput>({ type: 'Offering', name: '', platform: 'xiaohongshu', brief: '', offeringKind: 'product' })
const detailLoading = ref(false)
const detailError = ref('')
const eventsLoading = ref(false)
const eventsError = ref('')
const pipelineTasks = ref<PipelineTask[]>([])
const selectedPipelineId = ref<string | null>(null)
const selectedPipelineDetail = ref<PipelineTask | null>(null)
const pipelineLoading = ref(false)
const pipelineError = ref('')
const pipelineSearch = ref('')
const pipelineStatusFilter = ref('all')
const pipelineDetailLoading = ref(false)
const pipelineDetailError = ref('')
const pipelineEventsLoading = ref(false)
const pipelineEventsError = ref('')
const publications = ref<PublicationRecord[]>([])
const publicationCapabilities = ref<PublicationCapability[]>([])
const analyticsCapabilities = ref<AnalyticsCapability[]>([])
const analyticsSummary = ref<AnalyticsSummary | null>(null)
const publicationLoading = ref(false)
const publicationError = ref('')
const publicationActionMessage = ref('')
const publicationDetailLoading = ref(false)
const publicationDetailError = ref('')
const publicationSyncing = ref(false)
const publicationToken = ref('')
const selectedPublicationId = ref<string | null>(null)
const candidate = ref<PublicationCandidate | null>(null)
const showCandidate = ref(false)
const candidateLoading = ref(false)
const candidateError = ref('')
const authorizeConfirmed = ref(false)
const commitConfirmed = ref(false)
const authorization = ref<ReturnType<typeof mapPublicationAuthorization> | null>(null)
const preparedPublication = ref<PublicationRecord | null>(null)
const publicationForm = ref<PublicationCandidateRequest>({ runId: '', creator: 'local-creator', account: '', platform: 'douyin', variantId: '' })
const policyVersion = ref('v1')
const showCommitConfirm = ref(false)
let publicationController: AbortController | undefined
let detailRequestNumber = 0
let detailController: AbortController | undefined

const selectedTask = computed(() => selectedDetail.value ?? tasks.value.find((task) => task.id === selectedId.value) ?? null)
const selectedPipeline = computed(() => selectedPipelineDetail.value ?? pipelineTasks.value.find((task) => task.id === selectedPipelineId.value) ?? null)
const selectedPublication = computed(() => publications.value.find((item) => item.id === selectedPublicationId.value) ?? null)
const hasPublicationToken = computed(() => Boolean(publicationToken.value.trim()))
const eligiblePublicationRuns = computed(() => tasks.value.filter((task) => task.status === 'media_ready'))
const publicationStats = computed(() => ({
  total: publications.value.length,
  published: publications.value.filter((item) => item.status === 'published').length,
  reviewing: publications.value.filter((item) => ['submitted', 'reviewing'].includes(item.status)).length,
  attention: publications.value.filter((item) => ['unknown', 'failed', 'rejected'].includes(item.status)).length,
}))

const filteredPipelines = computed(() => {
  const query = pipelineSearch.value.trim().toLowerCase()
  return pipelineTasks.value.filter((pipeline) => {
    const searchable = [pipeline.title, pipeline.id, pipeline.researchTopic.title, ...pipeline.children.map((child) => `${child.platformLabel} ${child.id}`)]
    return (!query || searchable.some((value) => value.toLowerCase().includes(query))) && (pipelineStatusFilter.value === 'all' || pipeline.status === pipelineStatusFilter.value)
  })
})

const filteredTasks = computed(() => {
  const query = search.value.trim().toLowerCase()
  return tasks.value.filter((task) => {
    const matchesSearch = !query || [task.title, task.id, task.typeLabel, task.platformLabel].some((value) => value.toLowerCase().includes(query))
    const matchesStatus = statusFilter.value === 'all' || task.status === statusFilter.value
    const matchesType = typeFilter.value === 'all' || task.type === typeFilter.value
    const matchesPlatform = platformFilter.value === 'all' || task.platform === platformFilter.value
    return matchesSearch && matchesStatus && matchesType && matchesPlatform
  })
})

const stats = computed(() => ({
  total: tasks.value.length,
  active: tasks.value.filter((task) => ['queued', 'running'].includes(task.status)).length,
  attention: tasks.value.filter((task) => ['blocked', 'failed'].includes(task.status)).length,
  ready: tasks.value.filter((task) => task.status === 'package_ready').length,
}))

const pipelineStats = computed(() => ({
  total: pipelineTasks.value.length,
  active: pipelineTasks.value.filter((pipeline) => ['queued', 'running'].includes(pipeline.status)).length,
  attention: pipelineTasks.value.filter((pipeline) => ['blocked', 'failed'].includes(pipeline.status)).length,
  ready: pipelineTasks.value.filter((pipeline) => pipeline.status === 'distribution_ready').length,
}))

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date)
}

function publicationLabel(status: RunTask['publications'][number]['status']): string {
  return {
    not_connected: '未接入',
    not_submitted: '未投稿',
    submitted: '已投稿',
    published: '已发布',
    reviewing: '审核中',
    action_required: '待确认',
    failed: '失败',
    unknown: '待核验',
  }[status]
}

function selectTask(task: RunTask): void {
  selectedId.value = task.id
  selectedDetail.value = null
  detailError.value = ''
  eventsError.value = ''
  void loadDetail(task.id)
}

function setView(view: 'runs' | 'pipeline' | 'publishing'): void {
  activeView.value = view
  if (view === 'pipeline' && !pipelineTasks.value.length && !pipelineLoading.value) void refreshPipeline()
  if (view === 'runs' && !tasks.value.length && !isLoading.value) void refresh()
  if (view === 'publishing' && hasPublicationToken.value && !publicationLoading.value) void refreshPublications()
}

function publicationRequestOptions(signal?: AbortSignal): { token: string; signal?: AbortSignal } {
  return { token: publicationToken.value, signal }
}

function clearPublicationDataWhenTokenRemoved(): void {
  publicationError.value = ''
  publicationActionMessage.value = ''
  if (!hasPublicationToken.value) {
    publications.value = []
    publicationCapabilities.value = []
    analyticsCapabilities.value = []
    analyticsSummary.value = null
    selectedPublicationId.value = null
  }
}

function publicationCapability(platform: Platform): PublicationCapability | undefined {
  return publicationCapabilities.value.find((item) => item.platform === platform)
}

function publicationCapabilityLabel(capability?: PublicationCapability): string {
  if (!capability) return '能力未返回'
  return capability.configured ? '已配置' : '未配置'
}

function analyticsCapability(platform: Platform): AnalyticsCapability | undefined {
  return analyticsCapabilities.value.find((item) => item.platform === platform)
}

function platformName(value: string): string {
  return ({ douyin: '抖音', xiaohongshu: '小红书', bilibili: 'B站' } as Record<string, string>)[value] || value
}

function analyticsStateLabel(state?: AnalyticsCapability['state']): string {
  return state === 'ready' ? '可同步' : state === 'blocked' ? '能力受限' : state === 'unavailable' ? '不可用' : '未返回'
}

function reviewStatusLabel(status?: string): string {
  return ({ submitted: '已提交', reviewing: '审核中', published: '已发布', rejected: '已拒绝', unknown: '待核验' } as Record<string, string>)[status || ''] || '未回读'
}

function metricEntries(publication: PublicationRecord): string {
  const metrics = publication.analytics?.latestMetrics?.metrics || {}
  return Object.entries(metrics).map(([key, value]) => `${key} ${value}`).join(' · ') || '暂无指标'
}

async function loadPublicationInsights(records: PublicationRecord[]): Promise<void> {
  if (!hasPublicationToken.value || !records.length) return
  const results = await Promise.allSettled(records.map(async (publication) => [publication.id, mapPublicationInsights(await getPublicationInsights(publication.id, publicationRequestOptions()))] as const))
  const insights = new Map(results.filter((result): result is PromiseFulfilledResult<readonly [string, ReturnType<typeof mapPublicationInsights>]> => result.status === 'fulfilled').map((result) => result.value))
  publications.value = publications.value.map((publication) => {
    const analytics = insights.get(publication.id)
    return analytics ? { ...publication, analytics: { ...analytics, capability: analyticsCapability(publication.package.platform) } } : publication
  })
}

function publicationStatusText(status: PublicationRecord['status']): string {
  return publicationStatusLabel(status)
}

function publicationTone(status: PublicationRecord['status']): string {
  return `tone-${publicationStatusTone(status)}`
}

function selectPublication(publication: PublicationRecord): void {
  selectedPublicationId.value = publication.id
  publicationDetailError.value = ''
  void loadPublicationDetail(publication.id)
}

async function loadPublicationDetail(id: string): Promise<void> {
  if (!hasPublicationToken.value) return
  publicationDetailLoading.value = true
  publicationDetailError.value = ''
  try {
    const detail = mapPublication(await getPublication(id, publicationRequestOptions()))
    const existing = publications.value.find((item) => item.id === id)
    publications.value = publications.value.map((item) => item.id === id ? { ...detail, analytics: existing?.analytics } : item)
  } catch (error) {
    publicationDetailError.value = error instanceof Error ? error.message : '无法读取发布详情'
  } finally {
    publicationDetailLoading.value = false
  }
}

async function refreshPublications(): Promise<void> {
  if (!hasPublicationToken.value) {
    clearPublicationDataWhenTokenRemoved()
    return
  }
  publicationLoading.value = true
  publicationError.value = ''
  publicationActionMessage.value = ''
  publicationController?.abort()
  publicationController = new AbortController()
  try {
    const [recordsResult, capabilitiesResult, analyticsCapabilitiesResult, analyticsSummaryResult] = await Promise.allSettled([
      getPublications(50, publicationRequestOptions(publicationController.signal)),
      getPublicationCapabilities(publicationRequestOptions(publicationController.signal)),
      getAnalyticsCapabilities(publicationRequestOptions(publicationController.signal)),
      getAnalyticsSummary(publicationRequestOptions(publicationController.signal)),
    ])
    if (recordsResult.status === 'fulfilled') {
      publications.value = mapPublications(recordsResult.value)
      await loadPublicationInsights(publications.value)
    }
    else if (recordsResult.reason?.name !== 'AbortError') publicationError.value = recordsResult.reason instanceof Error ? recordsResult.reason.message : '无法读取发布记录'
    if (capabilitiesResult.status === 'fulfilled') publicationCapabilities.value = mapPublicationCapabilities(capabilitiesResult.value)
    else if (capabilitiesResult.reason?.name !== 'AbortError') publicationError.value = publicationError.value || (capabilitiesResult.reason instanceof Error ? capabilitiesResult.reason.message : '无法读取平台能力')
    if (analyticsCapabilitiesResult.status === 'fulfilled') {
      analyticsCapabilities.value = mapAnalyticsCapabilities(analyticsCapabilitiesResult.value)
      publications.value = publications.value.map((publication) => publication.analytics ? { ...publication, analytics: { ...publication.analytics, capability: analyticsCapability(publication.package.platform) } } : publication)
    } else if (analyticsCapabilitiesResult.reason?.name !== 'AbortError') publicationError.value = publicationError.value || (analyticsCapabilitiesResult.reason instanceof Error ? analyticsCapabilitiesResult.reason.message : '无法读取指标能力')
    if (analyticsSummaryResult.status === 'fulfilled') analyticsSummary.value = mapAnalyticsSummary(analyticsSummaryResult.value)
    else if (analyticsSummaryResult.reason?.name !== 'AbortError') publicationError.value = publicationError.value || (analyticsSummaryResult.reason instanceof Error ? analyticsSummaryResult.reason.message : '无法读取指标汇总')
    if (selectedPublicationId.value && !publications.value.some((item) => item.id === selectedPublicationId.value)) selectedPublicationId.value = null
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return
    publicationError.value = error instanceof Error ? error.message : '无法读取发布状态'
  } finally {
    publicationLoading.value = false
  }
}

async function syncPublication(publication: PublicationRecord): Promise<void> {
  if (!hasPublicationToken.value) return
  publicationSyncing.value = true
  publicationError.value = ''
  try {
    await syncPublicationInsights(publication.id, publicationRequestOptions())
    publicationActionMessage.value = '已手动发起审核与指标同步；页面不会自动再次同步。'
    await refreshPublications()
    if (selectedPublicationId.value === publication.id) await loadPublicationDetail(publication.id)
  } catch (error) {
    publicationError.value = error instanceof Error ? error.message : '手动同步失败'
  } finally {
    publicationSyncing.value = false
  }
}

function openCandidateForm(): void {
  candidate.value = null
  authorization.value = null
  preparedPublication.value = null
  candidateError.value = ''
  authorizeConfirmed.value = false
  commitConfirmed.value = false
  const firstRun = eligiblePublicationRuns.value[0]
  const platform = firstRun?.platform === 'douyin' || firstRun?.platform === 'xiaohongshu' || firstRun?.platform === 'bilibili' ? firstRun.platform : 'douyin'
  publicationForm.value = {
    runId: firstRun?.id || '',
    creator: 'local-creator',
    account: firstRun ? getRunPublicationAccounts(firstRun, platform)[0] || '' : '',
    platform,
    variantId: firstRun ? getRunPublicationVariants(firstRun)[0] || '' : '',
  }
  showCandidate.value = true
}

function updateCandidateRun(): void {
  const task = eligiblePublicationRuns.value.find((item) => item.id === publicationForm.value.runId)
  if (!task) return
  const accounts = getRunPublicationAccounts(task, publicationForm.value.platform)
  const variants = getRunPublicationVariants(task)
  if (accounts.length) publicationForm.value.account = accounts[0]
  if (variants.length) publicationForm.value.variantId = variants[0]
}

function publicationRunAccounts(): string[] {
  const task = eligiblePublicationRuns.value.find((item) => item.id === publicationForm.value.runId)
  return task ? getRunPublicationAccounts(task, publicationForm.value.platform) : []
}

function publicationRunVariants(): string[] {
  const task = eligiblePublicationRuns.value.find((item) => item.id === publicationForm.value.runId)
  return task ? getRunPublicationVariants(task) : []
}

async function createCandidate(): Promise<void> {
  if (!hasPublicationToken.value) {
    candidateError.value = '服务端令牌未提供'
    return
  }
  const input = publicationForm.value
  if (!input.runId || !input.creator.trim() || !input.account.trim()) {
    candidateError.value = '请选择 media_ready Run，并填写创作者和平台账号'
    return
  }
  candidateLoading.value = true
  candidateError.value = ''
  try {
    candidate.value = mapPublicationCandidate(await createPublicationCandidate({ ...input, creator: input.creator.trim(), account: input.account.trim(), variantId: input.variantId?.trim() || undefined }, publicationRequestOptions()))
  } catch (error) {
    candidateError.value = error instanceof Error ? error.message : '无法生成发布候选'
  } finally {
    candidateLoading.value = false
  }
}

function policy(): { version: string; selected: PublicationCandidateRequest['platform'][] } {
  return { version: policyVersion.value, selected: [publicationForm.value.platform] }
}

async function authorizeAndPrepare(): Promise<void> {
  if (!candidate.value || !authorizeConfirmed.value || !hasPublicationToken.value) return
  candidateLoading.value = true
  candidateError.value = ''
  try {
    const expiresAt = new Date(Date.now() + 10 * 60 * 1000).toISOString()
    authorization.value = mapPublicationAuthorization(await authorizePublication({ candidate: candidate.value.request, confirm: true, policy: policy(), expiresAt }, publicationRequestOptions()))
    preparedPublication.value = mapPublication(await preparePublication({ candidate: candidate.value.request, authorizationId: authorization.value.id, fingerprint: candidate.value.fingerprint, policy: policy() }, publicationRequestOptions()))
    selectedPublicationId.value = preparedPublication.value.id
    showCandidate.value = false
    publicationActionMessage.value = '已授权并生成草稿。最终发布仍需单独确认。'
    await refreshPublications()
  } catch (error) {
    candidateError.value = error instanceof Error ? error.message : '授权或准备草稿失败'
  } finally {
    candidateLoading.value = false
  }
}

async function commitSelectedPublication(): Promise<void> {
  const item = selectedPublication.value || preparedPublication.value
  if (!item || !commitConfirmed.value || !hasPublicationToken.value) return
  candidateLoading.value = true
  publicationError.value = ''
  try {
    await commitPublication(item.id, item.fingerprint, publicationRequestOptions())
    commitConfirmed.value = false
    showCommitConfirm.value = false
    publicationActionMessage.value = '已向平台发起提交请求；是否成功以平台返回的 durable 证据为准。'
    await refreshPublications()
    await loadPublicationDetail(item.id)
  } catch (error) {
    publicationError.value = error instanceof Error ? error.message : '提交发布失败'
  } finally {
    candidateLoading.value = false
  }
}

async function reconcileSelectedPublication(): Promise<void> {
  const item = selectedPublication.value
  if (!item || item.status !== 'unknown' || !hasPublicationToken.value) return
  candidateLoading.value = true
  publicationError.value = ''
  try {
    await reconcilePublication(item.id, publicationRequestOptions())
    publicationActionMessage.value = '已发起只读核验，不会自动重试发布。'
    await refreshPublications()
    await loadPublicationDetail(item.id)
  } catch (error) {
    publicationError.value = error instanceof Error ? error.message : '核验发布状态失败'
  } finally {
    candidateLoading.value = false
  }
}

function selectPipeline(pipeline: PipelineTask): void {
  selectedPipelineId.value = pipeline.id
  selectedPipelineDetail.value = null
  pipelineDetailError.value = ''
  pipelineEventsError.value = ''
  void loadPipelineDetail(pipeline.id)
}

async function loadPipelineDetail(id: string): Promise<void> {
  const requestNumber = ++detailRequestNumber
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  pipelineDetailLoading.value = true
  pipelineEventsLoading.value = true
  pipelineDetailError.value = ''
  pipelineEventsError.value = ''
  const [detailResult, eventsResult] = await Promise.allSettled([
    getPipelineRun(id, controller.signal),
    getPipelineRunEvents(id, 0, controller.signal),
  ])
  if (requestNumber !== detailRequestNumber || selectedPipelineId.value !== id) return
  if (detailResult.status === 'fulfilled') selectedPipelineDetail.value = mapPipelineRun(detailResult.value)
  else if (detailResult.reason?.name !== 'AbortError') pipelineDetailError.value = detailResult.reason instanceof Error ? detailResult.reason.message : '无法读取研究流水线详情'
  if (eventsResult.status === 'fulfilled') {
    const events = mapEvents(eventsResult.value)
    if (selectedPipelineDetail.value) selectedPipelineDetail.value.events = events
  } else if (eventsResult.reason?.name !== 'AbortError') pipelineEventsError.value = eventsResult.reason instanceof Error ? eventsResult.reason.message : '无法读取研究流水线事件'
  pipelineDetailLoading.value = false
  pipelineEventsLoading.value = false
}

async function refreshPipeline(): Promise<void> {
  pipelineLoading.value = true
  pipelineError.value = ''
  try {
    pipelineTasks.value = mapPipelineRuns(await getPipelineRuns())
    if (selectedPipelineId.value && !pipelineTasks.value.some((pipeline) => pipeline.id === selectedPipelineId.value)) {
      selectedPipelineId.value = null
      selectedPipelineDetail.value = null
    } else if (selectedPipelineId.value) await loadPipelineDetail(selectedPipelineId.value)
  } catch (error) {
    pipelineTasks.value = []
    pipelineError.value = error instanceof Error ? error.message : '无法读取研究流水线列表'
  } finally {
    pipelineLoading.value = false
  }
}

async function loadDetail(id: string): Promise<void> {
  const requestNumber = ++detailRequestNumber
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  detailLoading.value = true
  eventsLoading.value = true
  detailError.value = ''
  eventsError.value = ''
  const [detailResult, eventsResult] = await Promise.allSettled([
    getRun(id, controller.signal),
    getRunEvents(id, 0, controller.signal),
  ])
  if (requestNumber !== detailRequestNumber || selectedId.value !== id) return

  if (detailResult.status === 'fulfilled') {
    selectedDetail.value = mapRun(detailResult.value)
  } else if (detailResult.reason?.name !== 'AbortError') {
    detailError.value = detailResult.reason instanceof Error ? detailResult.reason.message : '无法读取任务详情'
  }
  if (eventsResult.status === 'fulfilled') {
    const events = mapEvents(eventsResult.value)
    if (selectedDetail.value) selectedDetail.value.events = events
  } else if (eventsResult.reason?.name !== 'AbortError') {
    eventsError.value = eventsResult.reason instanceof Error ? eventsResult.reason.message : '无法读取任务事件'
  }
  detailLoading.value = false
  eventsLoading.value = false
}

async function refresh(): Promise<void> {
  isLoading.value = true
  errorMessage.value = ''
  actionMessage.value = ''
  try {
    tasks.value = mapRuns(await getRuns())
    if (selectedId.value && !tasks.value.some((task) => task.id === selectedId.value)) {
      selectedId.value = null
      selectedDetail.value = null
    } else if (selectedId.value) {
      await loadDetail(selectedId.value)
    }
  } catch (error) {
    tasks.value = []
    errorMessage.value = error instanceof Error ? error.message : '无法读取任务列表'
  } finally {
    isLoading.value = false
  }
}

async function runAction(action: 'cancel' | 'resume' | 'retry'): Promise<void> {
  if (!selectedTask.value) return
  isMutating.value = true
  errorMessage.value = ''
  actionMessage.value = ''
  try {
    const id = selectedTask.value.id
    if (action === 'cancel') await cancelRun(id)
    if (action === 'resume') await resumeRun(id)
    if (action === 'retry') await retryRun(id)
    actionMessage.value = '操作已提交，正在刷新状态'
    await refresh()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '操作未完成'
  } finally {
    isMutating.value = false
  }
}

async function submitCreate(): Promise<void> {
  if (!createForm.value.name.trim()) return
  isMutating.value = true
  errorMessage.value = ''
  try {
    const response = await createRun({ ...createForm.value, name: createForm.value.name.trim() })
    const created = mapRun(response)
    showCreate.value = false
    createForm.value = { type: 'Offering', name: '', platform: 'xiaohongshu', brief: '', offeringKind: 'product' }
    await refresh()
    if (created.id !== 'unknown-0' && tasks.value.some((task) => task.id === created.id)) selectTask(created)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '创建任务失败'
  } finally {
    isMutating.value = false
  }
}

onMounted(() => { void refresh() })
onBeforeUnmount(() => detailController?.abort())
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="brand-lockup">
        <div class="brand-mark"><Archive :size="16" /></div>
        <div>
          <div class="brand-name">JUDGMENT MEDIA</div>
          <div class="brand-context">本地任务与发布看板</div>
        </div>
      </div>
      <div class="topbar-meta"><span class="connection-dot" /> API {{ activeView === 'pipeline' ? '/api/v1/pipeline-runs' : activeView === 'publishing' ? '/api/v1/publications' : '/api/v1/runs' }}</div>
    </header>

    <div class="workspace">
      <aside class="sidebar">
        <div class="sidebar-section-label">工作区</div>
        <button class="nav-item" :class="{ active: activeView === 'runs' }" @click="setView('runs')">
          <LoaderCircle :size="16" /> 内容分发
          <span class="nav-count">{{ stats.total }}</span>
        </button>
        <button class="nav-item" :class="{ active: activeView === 'pipeline' }" @click="setView('pipeline')">
          <GitBranch :size="16" /> 研究流水线
          <span class="nav-count">{{ pipelineStats.total }}</span>
        </button>
        <button class="nav-item" :class="{ active: activeView === 'publishing' }" @click="setView('publishing')">
          <Send :size="16" /> 发布状态
        </button>
        <div class="sidebar-foot">
          <div class="sidebar-section-label">连接</div>
          <div class="api-health"><span class="status-dot" :class="{ bad: Boolean(errorMessage) }" />{{ errorMessage ? '接口不可用' : '本地 API' }}</div>
          <div class="sidebar-note">生产环境使用同源 API<br />开发代理：127.0.0.1:8088</div>
        </div>
      </aside>

      <main class="main-content">
        <section class="page-heading">
          <div>
            <div class="eyebrow">OPERATIONS / {{ activeView === 'runs' ? 'DISTRIBUTION' : activeView === 'pipeline' ? 'PIPELINE' : 'PUBLICATIONS' }}</div>
            <h1>{{ activeView === 'runs' ? '内容分发' : activeView === 'pipeline' ? '研究流水线' : '发布状态' }}</h1>
            <p>{{ activeView === 'runs' ? '观察内容分发 Run、阻塞原因与可交付产物。' : activeView === 'pipeline' ? '观察研究主题、内容原子与分发子运行的真实状态。' : '按任务查看各平台真实返回的投稿状态。' }}</p>
          </div>
          <div class="heading-actions">
            <button class="icon-button" title="刷新" :disabled="isLoading || pipelineLoading" @click="activeView === 'pipeline' ? refreshPipeline() : refresh()"><RefreshCw :size="17" :class="{ spin: isLoading || pipelineLoading }" /></button>
            <button v-if="activeView === 'runs'" class="primary-button" @click="showCreate = true"><Plus :size="16" /> 新建任务</button>
          </div>
        </section>

        <div v-if="errorMessage || pipelineError" class="error-banner"><AlertCircle :size="17" /><span>{{ activeView === 'pipeline' ? (pipelineError || errorMessage) : errorMessage }}</span><button class="banner-close" title="关闭提示" @click="activeView === 'pipeline' ? pipelineError = '' : errorMessage = ''"><X :size="15" /></button></div>
        <div v-if="actionMessage" class="success-banner"><Check :size="17" /><span>{{ actionMessage }}</span></div>

        <section v-if="activeView === 'runs'" class="content-stack">
          <div class="metric-strip">
            <div class="metric"><span>全部分发 Run</span><strong>{{ stats.total }}</strong></div>
            <div class="metric"><span>处理中</span><strong class="teal-text">{{ stats.active }}</strong></div>
            <div class="metric"><span>需处理</span><strong class="amber-text">{{ stats.attention }}</strong></div>
            <div class="metric"><span>产物就绪</span><strong class="green-text">{{ stats.ready }}</strong></div>
          </div>

          <section class="toolbar">
            <div class="search-box"><Search :size="16" /><input v-model="search" placeholder="搜索任务名或 ID" /></div>
            <div class="filter-group"><Filter :size="15" /><select v-model="statusFilter"><option value="all">全部状态</option><option value="queued">排队中</option><option value="running">执行中</option><option value="blocked">已阻塞</option><option value="failed">失败</option><option value="package_ready">产物已就绪</option><option value="media_ready">媒体已就绪</option><option value="cancelled">已取消</option></select><select v-model="typeFilter"><option value="all">全部类型</option><option value="Offering">Offering</option><option value="ContentAtom">ContentAtom</option></select><select v-model="platformFilter"><option value="all">全部平台</option><option value="douyin">抖音</option><option value="xiaohongshu">小红书</option><option value="bilibili">B站</option></select></div>
          </section>

          <section class="board-layout">
            <div class="table-wrap">
              <div class="table-head"><span>任务</span><span>类型</span><span>平台</span><span>状态</span><span>进度</span><span>更新</span></div>
              <div v-if="isLoading && !tasks.length" class="empty-state"><LoaderCircle :size="22" class="spin" /><strong>正在读取任务</strong><span>等待 /api/v1/runs 返回真实数据</span></div>
              <div v-else-if="!filteredTasks.length" class="empty-state"><FileBox :size="24" /><strong>{{ tasks.length ? '没有匹配任务' : '暂无任务' }}</strong><span>{{ tasks.length ? '调整筛选条件后重试' : 'API 返回为空，尚未有可展示的运行记录' }}</span></div>
              <button v-for="task in filteredTasks" :key="task.id" class="task-row" :class="{ selected: selectedId === task.id }" @click="selectTask(task)">
                <span class="task-primary"><strong>{{ task.title }}</strong><small>{{ task.id }}</small></span>
                <span><span class="type-chip">{{ task.typeLabel }}</span></span>
                <span class="platform-text">{{ task.platformLabel }}</span>
                <span><span class="status-badge" :class="`tone-${task.statusTone}`"><span class="status-dot" />{{ task.statusLabel }}</span></span>
                <span class="progress-cell"><span v-if="task.progress !== null" class="progress-track"><span :style="{ width: `${task.progress}%` }" :class="`progress-${task.statusTone}`" /></span><small>{{ task.progressLabel }}</small></span>
                <span class="date-text">{{ formatDate(task.updatedAt || task.createdAt) }}<ChevronRight :size="15" /></span>
              </button>
            </div>

            <aside class="detail-panel">
              <div v-if="selectedTask" class="detail-content">
                <div class="detail-header"><div><div class="detail-kicker">RUN DETAIL</div><h2>{{ selectedTask.title }}</h2><div class="detail-id">{{ selectedTask.id }}</div></div><button class="icon-button small" title="关闭详情" @click="selectedId = null; selectedDetail = null"><X :size="15" /></button></div>
                <div v-if="detailLoading" class="detail-loading"><LoaderCircle :size="14" class="spin" />正在读取详情与事件</div>
                <div v-if="detailError" class="inline-error"><AlertCircle :size="14" />{{ detailError }}</div>
                <div class="detail-status-line"><span class="status-badge" :class="`tone-${selectedTask.statusTone}`"><span class="status-dot" />{{ selectedTask.statusLabel }}</span><span>{{ selectedTask.platformLabel }} · {{ selectedTask.typeLabel }} · 第 {{ selectedTask.attempt }} 次</span></div>
                <div v-if="selectedTask.blockingReason && ['blocked', 'failed'].includes(selectedTask.status)" class="blocking-box"><CircleAlert :size="16" /><div><strong>{{ selectedTask.status === 'blocked' ? '阻塞原因' : '失败原因' }}</strong><p>{{ selectedTask.blockingReason }}</p></div></div>
                <div v-if="selectedTask.capabilityBlock && selectedTask.status !== 'blocked'" class="capability-box"><CircleAlert :size="16" /><div><strong>能力边界 · {{ selectedTask.capabilityBlock.capability || selectedTask.capabilityBlock.code }}</strong><p>{{ selectedTask.capabilityBlock.message }}</p></div></div>
                <div v-if="selectedTask.runError && !selectedTask.blockingReason" class="blocking-box"><AlertCircle :size="16" /><div><strong>运行错误 · {{ selectedTask.runError.code }}</strong><p>{{ selectedTask.runError.message }}</p></div></div>
                <div class="detail-actions"><button v-if="canCancel(selectedTask.status)" class="secondary-button" :disabled="isMutating" @click="runAction('cancel')"><Square :size="14" /> 取消</button><button v-if="canResume(selectedTask.status)" class="secondary-button" :disabled="isMutating" @click="runAction('resume')"><CirclePlay :size="14" /> 恢复</button><button v-if="canRetry(selectedTask.status)" class="secondary-button" :disabled="isMutating" @click="runAction('retry')"><RotateCcw :size="14" /> 重试</button></div>

                <div class="detail-section"><div class="section-title"><span>步骤</span><small>{{ selectedTask.steps.length ? `${selectedTask.steps.length} 个步骤` : '暂无步骤数据' }}</small></div><div v-if="selectedTask.steps.length" class="step-list"><div v-for="step in selectedTask.steps" :key="step.id" class="step-item"><span class="step-icon" :class="`tone-${step.statusTone || 'neutral'}`"><Check v-if="['package_ready', 'media_ready'].includes(step.status)" :size="13" /><LoaderCircle v-else-if="step.status === 'running'" :size="13" class="spin" /><CirclePause v-else-if="step.status === 'blocked'" :size="13" /><CircleAlert v-else-if="step.status === 'failed'" :size="13" /><Clock3 v-else :size="13" /></span><div><strong>{{ step.name }}</strong><small>{{ step.detail || getStepStatus(step.status) }}</small></div></div></div><div v-else class="inline-empty">后端尚未返回步骤信息</div></div>

                <div class="detail-section"><div class="section-title"><span>事件</span><small>{{ eventsLoading ? '正在读取' : selectedTask.events.length ? `${selectedTask.events.length} 条` : '暂无事件' }}</small></div><div v-if="eventsError" class="inline-error"><AlertCircle :size="14" />{{ eventsError }}</div><div v-else-if="selectedTask.events.length" class="event-list"><div v-for="event in selectedTask.events" :key="event.id" class="event-item"><span class="event-marker" :class="`event-${event.level}`" /><div><p>{{ event.message }}</p><small>{{ event.type }} · {{ formatDate(event.at) }}<span v-if="Object.keys(event.payload).length"> · {{ JSON.stringify(event.payload) }}</span></small></div></div></div><div v-else-if="!eventsLoading" class="inline-empty">后端尚未返回事件信息</div></div>

                <div class="detail-section"><div class="section-title"><span>产物</span><small>{{ selectedTask.artifacts.length ? `${selectedTask.artifacts.length} 个` : '暂无产物' }}</small></div><div v-if="selectedTask.artifacts.length" class="artifact-list"><a v-for="artifact in selectedTask.artifacts" :key="artifact.id" class="artifact-item" :href="artifact.url || undefined" target="_blank" rel="noreferrer"><FilePlus2 :size="15" /><span><strong>{{ artifact.name }}</strong><small>{{ artifact.kind || artifact.path || '已返回产物记录' }}</small></span><ChevronRight :size="14" /></a></div><div v-else class="inline-empty">任务尚未返回可用产物</div></div>

                <div class="detail-section"><div class="section-title"><span>平台投稿</span><small>真实响应</small></div><div class="publication-grid"><div v-for="publication in selectedTask.publications" :key="publication.platform" class="publication-row"><span>{{ publication.label }}</span><span class="publication-status" :class="`pub-${publication.status}`">{{ publicationLabel(publication.status) }}</span></div></div></div>
              </div>
              <div v-else class="detail-placeholder"><ArrowLeft :size="20" /><strong>选择一个任务</strong><span>查看步骤、事件、产物与真实投稿状态</span></div>
            </aside>
          </section>
        </section>

        <section v-else-if="activeView === 'pipeline'" class="content-stack">
          <div class="metric-strip">
            <div class="metric"><span>全部流水线</span><strong>{{ pipelineStats.total }}</strong></div>
            <div class="metric"><span>处理中</span><strong class="teal-text">{{ pipelineStats.active }}</strong></div>
            <div class="metric"><span>需处理</span><strong class="amber-text">{{ pipelineStats.attention }}</strong></div>
            <div class="metric"><span>分发产物就绪</span><strong class="green-text">{{ pipelineStats.ready }}</strong></div>
          </div>
          <section class="toolbar">
            <div class="search-box"><Search :size="16" /><input v-model="pipelineSearch" placeholder="搜索研究主题、流水线 ID 或子运行" /></div>
            <div class="filter-group"><Filter :size="15" /><select v-model="pipelineStatusFilter"><option value="all">全部状态</option><option value="queued">排队中</option><option value="running">执行中</option><option value="blocked">已阻塞</option><option value="failed">失败</option><option value="distribution_ready">分发产物已就绪</option><option value="cancelled">已取消</option></select></div>
          </section>
          <section class="board-layout pipeline-board">
            <div class="table-wrap pipeline-list">
              <div class="table-head pipeline-head"><span>研究主题</span><span>状态</span><span>原子</span><span>子运行</span><span>更新</span></div>
              <div v-if="pipelineLoading && !pipelineTasks.length" class="empty-state"><LoaderCircle :size="22" class="spin" /><strong>正在读取研究流水线</strong><span>等待 /api/v1/pipeline-runs 返回真实数据</span></div>
              <div v-else-if="!filteredPipelines.length" class="empty-state"><GitBranch :size="24" /><strong>{{ pipelineTasks.length ? '没有匹配流水线' : '暂无研究流水线' }}</strong><span>{{ pipelineTasks.length ? '调整筛选条件后重试' : 'API 返回为空，尚未有可展示的流水线记录' }}</span></div>
              <button v-for="pipeline in filteredPipelines" :key="pipeline.id" class="task-row pipeline-row" :class="{ selected: selectedPipelineId === pipeline.id }" @click="selectPipeline(pipeline)">
                <span class="task-primary"><strong>{{ pipeline.researchTopic.title }}</strong><small>{{ pipeline.id }}</small></span>
                <span><span class="status-badge" :class="`tone-${pipeline.statusTone}`"><span class="status-dot" />{{ pipeline.statusLabel }}</span></span>
                <span class="platform-text">{{ pipeline.atomsCount }}</span>
                <span class="pipeline-children"><span v-for="child in pipeline.children.slice(0, 3)" :key="child.id" class="child-chip" :title="child.id">{{ child.platformLabel }} · {{ child.statusLabel }}</span><small v-if="pipeline.children.length > 3">+{{ pipeline.children.length - 3 }}</small></span>
                <span class="date-text">{{ formatDate(pipeline.updatedAt || pipeline.createdAt) }}<ChevronRight :size="15" /></span>
              </button>
            </div>
            <aside class="detail-panel">
              <div v-if="selectedPipeline" class="detail-content">
                <div class="detail-header"><div><div class="detail-kicker">PIPELINE DETAIL</div><h2>{{ selectedPipeline.researchTopic.title }}</h2><div class="detail-id">{{ selectedPipeline.id }}</div></div><button class="icon-button small" title="关闭详情" @click="selectedPipelineId = null; selectedPipelineDetail = null"><X :size="15" /></button></div>
                <div v-if="pipelineDetailLoading" class="detail-loading"><LoaderCircle :size="14" class="spin" />正在读取详情与事件</div>
                <div v-if="pipelineDetailError" class="inline-error"><AlertCircle :size="14" />{{ pipelineDetailError }}</div>
                <div class="detail-status-line"><span class="status-badge" :class="`tone-${selectedPipeline.statusTone}`"><span class="status-dot" />{{ selectedPipeline.statusLabel }}</span><span>{{ selectedPipeline.atomsCount }} 个内容原子 · 第 {{ selectedPipeline.attempt }} 次</span></div>
                <div v-if="selectedPipeline.capabilityBlock" class="capability-box"><CircleAlert :size="16" /><div><strong>能力阻塞 · {{ selectedPipeline.capabilityBlock.capability || selectedPipeline.capabilityBlock.code }}</strong><p>{{ selectedPipeline.capabilityBlock.message }}</p></div></div>
                <div v-if="selectedPipeline.errorMessage" class="blocking-box"><AlertCircle :size="16" /><div><strong>流水线错误</strong><p>{{ selectedPipeline.errorMessage }}</p></div></div>
                <div class="detail-section"><div class="section-title"><span>研究主题</span><small>真实 API</small></div><div class="pipeline-topic"><strong>{{ selectedPipeline.researchTopic.title }}</strong><p v-if="selectedPipeline.researchTopic.question">{{ selectedPipeline.researchTopic.question }}</p><small v-if="selectedPipeline.researchTopic.source">来源：{{ selectedPipeline.researchTopic.source }}</small></div></div>
                <div class="detail-section"><div class="section-title"><span>内容原子</span><small>{{ selectedPipeline.atoms.length }} 个</small></div><div v-if="selectedPipeline.atoms.length" class="atom-list"><div v-for="atom in selectedPipeline.atoms" :key="atom.id" class="atom-item"><strong>{{ atom.title }}</strong><small>{{ atom.id }}<span v-if="atom.status"> · {{ atom.status }}</span></small></div></div><div v-else class="inline-empty">后端尚未返回内容原子</div></div>
                <div class="detail-section"><div class="section-title"><span>分发子运行</span><small>{{ selectedPipeline.children.length }} 个</small></div><div v-if="selectedPipeline.children.length" class="child-list"><div v-for="child in selectedPipeline.children" :key="child.id" class="publication-row"><span>{{ child.platformLabel }} <small class="detail-id">{{ child.id }}</small></span><span class="status-badge" :class="`tone-${child.statusTone}`">{{ child.statusLabel }}</span></div></div><div v-else class="inline-empty">后端尚未返回子运行</div></div>
                <div class="detail-section"><div class="section-title"><span>事件</span><small>{{ pipelineEventsLoading ? '正在读取' : selectedPipeline.events.length ? `${selectedPipeline.events.length} 条` : '暂无事件' }}</small></div><div v-if="pipelineEventsError" class="inline-error"><AlertCircle :size="14" />{{ pipelineEventsError }}</div><div v-else-if="selectedPipeline.events.length" class="event-list"><div v-for="event in selectedPipeline.events" :key="event.id" class="event-item"><span class="event-marker" :class="`event-${event.level}`" /><div><p>{{ event.message }}</p><small>{{ event.type }} · {{ formatDate(event.at) }}<span v-if="Object.keys(event.payload).length"> · {{ JSON.stringify(event.payload) }}</span></small></div></div></div><div v-else-if="!pipelineEventsLoading" class="inline-empty">后端尚未返回事件信息</div></div>
              </div>
              <div v-else class="detail-placeholder"><ArrowLeft :size="20" /><strong>选择一条研究流水线</strong><span>查看研究主题、内容原子、子运行与事件</span></div>
            </aside>
          </section>
        </section>

        <section v-else class="content-stack publication-stack">
          <section class="publication-token-section">
            <div class="section-title"><span>服务端令牌</span><small>仅保存在当前页面内存</small></div>
            <div class="token-row">
              <input v-model="publicationToken" type="password" autocomplete="off" placeholder="输入服务端令牌" @input="clearPublicationDataWhenTokenRemoved" />
              <button class="secondary-button" :disabled="publicationLoading || !hasPublicationToken" @click="refreshPublications"><RefreshCw :size="14" :class="{ spin: publicationLoading }" />读取发布状态</button>
            </div>
            <div v-if="!hasPublicationToken" class="form-note token-note"><AlertCircle :size="14" />服务端令牌未提供</div>
          </section>
          <div class="metric-strip">
            <div class="metric"><span>发布记录</span><strong>{{ publicationStats.total }}</strong></div>
            <div class="metric"><span>已发布</span><strong class="green-text">{{ publicationStats.published }}</strong></div>
            <div class="metric"><span>审核中</span><strong class="teal-text">{{ publicationStats.reviewing }}</strong></div>
            <div class="metric"><span>需核验/处理</span><strong class="amber-text">{{ publicationStats.attention }}</strong></div>
          </div>

          <section class="publication-section">
            <div class="section-title"><span>平台能力</span><small>只显示后端真实返回</small></div>
            <div class="capability-grid">
              <div v-for="capability in publicationCapabilities" :key="capability.platform" class="capability-row">
                <div><strong>{{ { douyin: '抖音', xiaohongshu: '小红书', bilibili: 'B站' }[capability.platform] }}</strong><small>{{ capability.configured ? '可用发布适配器' : (capability.reason || '后端尚未配置') }}</small></div>
                <span class="status-badge" :class="capability.configured ? 'tone-green' : 'tone-neutral'"><span class="status-dot" :class="{ bad: !capability.configured }" />{{ publicationCapabilityLabel(capability) }}</span>
              </div>
              <div v-if="!publicationCapabilities.length && !publicationLoading" class="inline-empty">后端没有返回平台能力。未返回不等于可发布。</div>
            </div>
          </section>

          <section class="publication-section">
            <div class="section-title"><span>审核与指标能力</span><small>不会自动同步</small></div>
            <div class="capability-grid">
              <div v-for="capability in analyticsCapabilities" :key="`${capability.provider}-${capability.platform}`" class="capability-row">
                <div><strong>{{ platformName(capability.platform) }} · {{ capability.provider }}</strong><small>{{ capability.reason || '后端真实返回的只读能力' }}</small></div>
                <span class="status-badge" :class="capability.state === 'ready' ? 'tone-green' : capability.state === 'blocked' ? 'tone-amber' : 'tone-neutral'"><span class="status-dot" :class="{ bad: capability.state !== 'ready' }" />{{ analyticsStateLabel(capability.state) }}</span>
              </div>
              <div v-if="!analyticsCapabilities.length && !publicationLoading" class="inline-empty">后端没有返回审核或指标能力，不会伪造回读结果。</div>
            </div>
            <div v-if="analyticsSummary" class="analytics-summary-line">已保存 {{ analyticsSummary.reviewSnapshots }} 条审核快照 · {{ analyticsSummary.metricSnapshots }} 条指标快照 · 最近观测 {{ formatDate(analyticsSummary.lastObservedAt) }}</div>
          </section>

          <section class="toolbar">
            <div class="toolbar-copy"><ShieldCheck :size="16" /><span>媒体已就绪只代表视频和封面可用，不代表已投稿或已发布。</span></div>
            <div class="heading-actions"><button class="secondary-button" :disabled="publicationLoading || !hasPublicationToken" @click="refreshPublications"><RefreshCw :size="14" :class="{ spin: publicationLoading }" />刷新状态</button><button class="primary-button" :disabled="!eligiblePublicationRuns.length || !hasPublicationToken" @click="openCandidateForm"><Plus :size="15" />从媒体 Run 创建候选</button></div>
          </section>

          <div v-if="publicationError" class="error-banner"><AlertCircle :size="17" /><span>{{ publicationError }}</span><button class="banner-close" title="关闭提示" @click="publicationError = ''"><X :size="15" /></button></div>
          <div v-if="publicationActionMessage" class="success-banner"><Check :size="17" /><span>{{ publicationActionMessage }}</span></div>

          <section class="board-layout publication-board">
            <div class="table-wrap publication-list">
              <div class="table-head publication-head"><span>发布记录</span><span>平台/账号</span><span>状态</span><span>外部证据</span><span>更新时间</span></div>
              <div v-if="publicationLoading && !publications.length" class="empty-state"><LoaderCircle :size="22" class="spin" /><strong>正在读取发布记录</strong><span>等待 /api/v1/publications 返回真实数据</span></div>
              <div v-else-if="!publications.length" class="empty-state"><Send :size="24" /><strong>暂无发布记录</strong><span>先选择一个 media_ready Run 创建候选，草稿和发布结果会在这里持续跟踪。</span></div>
              <button v-for="publication in publications" :key="publication.id" class="task-row publication-row-grid" :class="{ selected: selectedPublicationId === publication.id }" @click="selectPublication(publication)">
                <span class="task-primary"><strong>{{ publication.package.title || '未命名内容' }}</strong><small>{{ publication.id }} · {{ publication.package.variantId }}</small></span>
                <span class="platform-text">{{ { douyin: '抖音', xiaohongshu: '小红书', bilibili: 'B站' }[publication.package.platform] }}<small class="detail-id">{{ publication.package.account }}</small></span>
                <span><span class="status-badge" :class="publicationTone(publication.status)"><span class="status-dot" />{{ publicationStatusText(publication.status) }}</span></span>
                <span class="publication-evidence"><strong>{{ reviewStatusLabel(publication.analytics?.latestReview?.status) }}</strong><small>{{ publication.analytics?.latestReview?.provider || '暂无数据源' }} · {{ formatDate(publication.analytics?.latestReview?.lastSeenAt || publication.analytics?.latestReview?.observedAt) }}</small></span>
                <span class="date-text">{{ formatDate(publication.updatedAt || publication.createdAt) }}<ChevronRight :size="15" /></span>
              </button>
            </div>

            <aside class="detail-panel">
              <div v-if="selectedPublication" class="detail-content">
                <div class="detail-header"><div><div class="detail-kicker">PUBLICATION DETAIL</div><h2>{{ selectedPublication.package.title || '未命名内容' }}</h2><div class="detail-id">{{ selectedPublication.id }}</div></div><button class="icon-button small" title="关闭详情" @click="selectedPublicationId = null"><X :size="15" /></button></div>
                <div v-if="publicationDetailLoading" class="detail-loading"><LoaderCircle :size="14" class="spin" />正在读取 durable 发布详情</div>
                <div v-if="publicationDetailError" class="inline-error"><AlertCircle :size="14" />{{ publicationDetailError }}</div>
                <div class="detail-status-line"><span class="status-badge" :class="publicationTone(selectedPublication.status)"><span class="status-dot" />{{ publicationStatusText(selectedPublication.status) }}</span><span>{{ { douyin: '抖音', xiaohongshu: '小红书', bilibili: 'B站' }[selectedPublication.package.platform] }} · {{ selectedPublication.package.account }}</span></div>

                <div class="detail-section publication-guard"><CircleHelp :size="16" /><div><strong>状态含义</strong><p>“草稿”是系统已准备好发布包；“已提交审核”是平台已返回提交证据；只有收到平台发布证据才显示“已发布”。</p></div></div>
                <div class="detail-section"><div class="section-title"><span>内容包摘要</span><small>不可变指纹</small></div><dl class="publication-meta"><div><dt>Run</dt><dd>{{ selectedPublication.package.run }}</dd></div><div><dt>变体</dt><dd>{{ selectedPublication.package.variantId }}</dd></div><div><dt>指纹</dt><dd class="hash-text">{{ selectedPublication.fingerprint }}</dd></div><div><dt>视频</dt><dd>{{ selectedPublication.package.video }}<small v-if="selectedPublication.package.videoSha256" class="hash-subline">SHA-256 {{ selectedPublication.package.videoSha256 }}</small></dd></div><div><dt>平台封面</dt><dd>{{ selectedPublication.package.platformCover || '暂无' }}<small v-if="selectedPublication.package.platformCoverSha256" class="hash-subline">SHA-256 {{ selectedPublication.package.platformCoverSha256 }}</small></dd></div><div><dt>通用封面</dt><dd>{{ selectedPublication.package.cover || '暂无' }}<small v-if="selectedPublication.package.coverSha256" class="hash-subline">SHA-256 {{ selectedPublication.package.coverSha256 }}</small></dd></div><div><dt>3:4 封面</dt><dd>{{ selectedPublication.package.cover34 || '暂无' }}<small v-if="selectedPublication.package.cover34Sha256" class="hash-subline">SHA-256 {{ selectedPublication.package.cover34Sha256 }}</small></dd></div><div><dt>4:3 封面</dt><dd>{{ selectedPublication.package.cover43 || '暂无' }}<small v-if="selectedPublication.package.cover43Sha256" class="hash-subline">SHA-256 {{ selectedPublication.package.cover43Sha256 }}</small></dd></div><div><dt>文案</dt><dd>{{ selectedPublication.package.copySummary || selectedPublication.package.copy || '暂无文案摘要' }}</dd></div><div v-if="selectedPublication.package.tags.length"><dt>关键词</dt><dd>{{ selectedPublication.package.tags.join(' · ') }}</dd></div></dl></div>

                <div class="detail-section"><div class="section-title"><span>审核与指标回读</span><small>只读，手动同步</small></div><dl class="publication-meta"><div><dt>审核状态</dt><dd>{{ reviewStatusLabel(selectedPublication.analytics?.latestReview?.status) }} · {{ selectedPublication.analytics?.latestReview?.verified ? '已核验' : '未核验' }}</dd></div><div><dt>数据源</dt><dd>{{ selectedPublication.analytics?.latestReview?.provider || selectedPublication.analytics?.latestMetrics?.provider || '暂无' }}</dd></div><div><dt>最近看到</dt><dd>{{ formatDate(selectedPublication.analytics?.latestReview?.lastSeenAt || selectedPublication.analytics?.latestMetrics?.lastSeenAt) }}</dd></div><div><dt>指标</dt><dd>{{ metricEntries(selectedPublication) }}</dd></div><div><dt>同步</dt><dd>{{ selectedPublication.analytics?.syncJob?.status || '尚未同步' }} · {{ analyticsStateLabel(selectedPublication.analytics?.capability?.state) }}</dd></div><div v-if="selectedPublication.analytics?.syncJob?.lastError"><dt>同步错误</dt><dd class="error-text">{{ selectedPublication.analytics.syncJob.lastError }}</dd></div></dl><div class="detail-actions"><button class="secondary-button" :disabled="publicationSyncing || !hasPublicationToken" @click="syncPublication(selectedPublication)"><RefreshCw :size="14" :class="{ spin: publicationSyncing }" />手动同步审核与指标</button><span class="form-note">不会自动同步，也不会改变发布状态。</span></div></div>
                <div class="detail-section"><div class="section-title"><span>平台证据</span><small>durable record</small></div><dl class="publication-meta"><div><dt>外部 ID</dt><dd>{{ selectedPublication.externalId || selectedPublication.analytics?.latestReview?.externalId || '暂无' }}</dd></div><div><dt>外部 URL</dt><dd><a v-if="selectedPublication.externalUrl || selectedPublication.analytics?.latestReview?.externalUrl" :href="selectedPublication.externalUrl || selectedPublication.analytics?.latestReview?.externalUrl" target="_blank" rel="noreferrer">{{ selectedPublication.externalUrl || selectedPublication.analytics?.latestReview?.externalUrl }} <ExternalLink :size="12" /></a><span v-else>暂无</span></dd></div><div><dt>证据</dt><dd>{{ selectedPublication.evidence || selectedPublication.analytics?.latestReview?.proof || '暂无核验描述' }}</dd></div><div v-if="selectedPublication.lastError"><dt>错误</dt><dd class="error-text">{{ selectedPublication.lastError }}</dd></div></dl></div>

                <div class="detail-actions publication-actions"><button v-if="selectedPublication.status === 'draft_ready'" class="primary-button" :disabled="candidateLoading || !hasPublicationToken" @click="showCommitConfirm = true"><Send :size="14" />准备最终提交</button><button v-if="selectedPublication.status === 'unknown'" class="secondary-button" :disabled="candidateLoading || !hasPublicationToken" @click="reconcileSelectedPublication"><RefreshCw :size="14" :class="{ spin: candidateLoading }" />只读核验</button><span v-if="selectedPublication.status === 'committing'" class="form-note"><LoaderCircle :size="13" class="spin" />平台提交进行中，请等待 durable 状态</span><span v-if="['submitted', 'reviewing', 'published'].includes(selectedPublication.status)" class="form-note"><CheckCircle2 :size="14" />平台已返回 {{ publicationStatusText(selectedPublication.status) }} 证据</span></div>
              </div>
              <div v-else class="detail-placeholder"><ArrowLeft :size="20" /><strong>选择一条发布记录</strong><span>查看草稿、平台证据、指纹与错误</span></div>
            </aside>
          </section>
        </section>
      </main>
    </div>

    <div v-if="showCreate" class="modal-backdrop" @click.self="showCreate = false"><section class="modal"><div class="modal-header"><div><div class="detail-kicker">NEW RUN</div><h2>新建任务</h2></div><button class="icon-button small" title="关闭" @click="showCreate = false"><X :size="16" /></button></div><div class="form-grid"><label>任务类型<select v-model="createForm.type"><option value="Offering">Offering</option><option value="ContentAtom">ContentAtom</option></select></label><label>目标平台<select v-model="createForm.platform"><option value="xiaohongshu">小红书</option><option value="douyin">抖音</option><option value="bilibili">B站</option><option value="wechat_channels">视频号</option></select></label><label v-if="createForm.type === 'Offering'">Offering 类型<select v-model="createForm.offeringKind"><option value="product">商品</option><option value="service">服务</option><option value="capability">能力</option></select></label><label class="full-width">任务名称<input v-model="createForm.name" autofocus placeholder="例如：夏季饮品内容批次" /></label><label class="full-width">Brief / 输入<textarea v-model="createForm.brief" rows="5" placeholder="填写后端任务所需的业务输入"></textarea></label></div><div class="modal-footer"><span class="form-note">创建后以 API 返回为准</span><div><button class="secondary-button" @click="showCreate = false">取消</button><button class="primary-button" :disabled="isMutating || !createForm.name.trim()" @click="submitCreate"><LoaderCircle v-if="isMutating" :size="15" class="spin" /><Plus v-else :size="15" />创建任务</button></div></div></section></div>
    <div v-if="showCandidate" class="modal-backdrop" @click.self="showCandidate = false"><section class="modal publication-modal"><div class="modal-header"><div><div class="detail-kicker">PUBLICATION CANDIDATE</div><h2>创建发布候选</h2></div><button class="icon-button small" title="关闭" @click="showCandidate = false"><X :size="16" /></button></div><div class="modal-body"><div class="form-note candidate-notice"><ShieldCheck :size="15" />仅允许选择 media_ready Run。此步骤只读取产物并生成指纹，不会授权，也不会发布。</div><div class="form-grid"><label class="full-width">媒体 Run<select v-model="publicationForm.runId" @change="updateCandidateRun"><option value="">请选择 media_ready Run</option><option v-for="run in eligiblePublicationRuns" :key="run.id" :value="run.id">{{ run.title }} · {{ run.id }}</option></select></label><label>平台<select v-model="publicationForm.platform" @change="updateCandidateRun"><option value="douyin">抖音</option><option value="xiaohongshu">小红书</option><option value="bilibili">B站</option></select></label><label>创作者<input v-model="publicationForm.creator" placeholder="creator" /></label><label>平台账号<span v-if="publicationRunAccounts().length" class="field-hint">后端 Run 已提供账号</span><select v-if="publicationRunAccounts().length" v-model="publicationForm.account"><option v-for="account in publicationRunAccounts()" :key="account" :value="account">{{ account }}</option></select><input v-else v-model="publicationForm.account" placeholder="平台账号 ID" /></label><label>内容变体<span v-if="publicationRunVariants().length" class="field-hint">可选</span><select v-if="publicationRunVariants().length" v-model="publicationForm.variantId"><option value="">默认变体</option><option v-for="variant in publicationRunVariants()" :key="variant" :value="variant">{{ variant }}</option></select><input v-else v-model="publicationForm.variantId" placeholder="可选，例如 v1" /></label></div><div v-if="candidateError" class="inline-error"><AlertCircle :size="14" />{{ candidateError }}</div><div v-if="candidate" class="candidate-preview"><div class="candidate-preview-head"><strong>候选已生成</strong><span class="status-badge tone-neutral">待授权</span></div><p class="candidate-fingerprint">{{ candidate.fingerprint }}</p><dl class="publication-meta"><div><dt>标题</dt><dd>{{ candidate.package.title || '暂无标题' }}</dd></div><div><dt>视频</dt><dd>{{ candidate.package.video }}<small v-if="candidate.package.videoSha256" class="hash-subline">SHA-256 {{ candidate.package.videoSha256 }}</small></dd></div><div><dt>平台封面</dt><dd>{{ candidate.package.platformCover || '暂无' }}<small v-if="candidate.package.platformCoverSha256" class="hash-subline">SHA-256 {{ candidate.package.platformCoverSha256 }}</small></dd></div><div><dt>通用封面</dt><dd>{{ candidate.package.cover || '暂无' }}<small v-if="candidate.package.coverSha256" class="hash-subline">SHA-256 {{ candidate.package.coverSha256 }}</small></dd></div><div><dt>3:4 封面</dt><dd>{{ candidate.package.cover34 || '暂无' }}<small v-if="candidate.package.cover34Sha256" class="hash-subline">SHA-256 {{ candidate.package.cover34Sha256 }}</small></dd></div><div><dt>4:3 封面</dt><dd>{{ candidate.package.cover43 || '暂无' }}<small v-if="candidate.package.cover43Sha256" class="hash-subline">SHA-256 {{ candidate.package.cover43Sha256 }}</small></dd></div><div><dt>文案</dt><dd>{{ candidate.package.copySummary || candidate.package.copy || '暂无文案' }}</dd></div></dl><label class="confirm-line"><input v-model="authorizeConfirmed" type="checkbox" />我已核对平台、账号、视频、封面和指纹，并明确授权创建这一次发布草稿。</label></div></div><div class="modal-footer"><span class="form-note">授权有效期由后端控制，页面不会自动授权。</span><div><button class="secondary-button" @click="showCandidate = false">取消</button><button v-if="!candidate" class="primary-button" :disabled="candidateLoading || !eligiblePublicationRuns.length || !hasPublicationToken" @click="createCandidate"><LoaderCircle v-if="candidateLoading" :size="15" class="spin" /><KeyRound v-else :size="15" />生成候选</button><button v-else class="primary-button" :disabled="candidateLoading || !authorizeConfirmed || !hasPublicationToken" @click="authorizeAndPrepare"><LoaderCircle v-if="candidateLoading" :size="15" class="spin" /><ShieldCheck v-else :size="15" />确认授权并准备草稿</button></div></div></section></div>
    <div v-if="showCommitConfirm && selectedPublication" class="modal-backdrop" @click.self="showCommitConfirm = false"><section class="modal confirm-modal"><div class="modal-header"><div><div class="detail-kicker">FINAL COMMIT</div><h2>确认提交到平台</h2></div><button class="icon-button small" title="关闭" @click="showCommitConfirm = false"><X :size="16" /></button></div><div class="modal-body"><div class="commit-warning"><CircleAlert :size="18" /><div><strong>这是第二次、独立的明确确认</strong><p>提交后将调用平台适配器。系统不会把草稿或 media_ready 当作已发布，最终状态必须由平台返回证据确认。</p></div></div><dl class="publication-meta"><div><dt>平台</dt><dd>{{ { douyin: '抖音', xiaohongshu: '小红书', bilibili: 'B站' }[selectedPublication.package.platform] }}</dd></div><div><dt>账号</dt><dd>{{ selectedPublication.package.account }}</dd></div><div><dt>平台封面</dt><dd>{{ selectedPublication.package.platformCover || '暂无' }}<small v-if="selectedPublication.package.platformCoverSha256" class="hash-subline">SHA-256 {{ selectedPublication.package.platformCoverSha256 }}</small></dd></div><div><dt>指纹</dt><dd class="hash-text">{{ selectedPublication.fingerprint }}</dd></div></dl><label class="confirm-line"><input v-model="commitConfirmed" type="checkbox" />我确认提交这份精确指纹对应的内容包，并承担平台发布后果。</label></div><div class="modal-footer"><span class="form-note">unknown 状态不可在这里重试，只能返回核验。</span><div><button class="secondary-button" @click="showCommitConfirm = false">取消</button><button class="primary-button" :disabled="candidateLoading || !commitConfirmed || !hasPublicationToken" @click="commitSelectedPublication"><Send :size="15" />确认最终提交</button></div></div></section></div>
  </div>
</template>

<script lang="ts">
import { getStatusLabel, getStatusTone } from './status'

export default {
  methods: {
    canCancel,
    canResume,
    canRetry,
    getStepStatus(status: RunTask['status']) {
      return getStatusLabel(status)
    },
    getStepTone(status: RunTask['status']) {
      return getStatusTone(status)
    },
  },
}
</script>
