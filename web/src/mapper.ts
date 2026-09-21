import { getPipelineStatusLabel, getPipelineStatusTone, getStatusLabel, getStatusTone, normalizePipelineStatus, normalizeStatus } from './status'
import type { AnalyticsCapability, AnalyticsMetricSnapshot, AnalyticsReviewSnapshot, AnalyticsSummary, AnalyticsSyncJob, CapabilityBlock, PipelineAtom, PipelineChildRun, PipelineTask, Platform, PublicationAnalytics, PublicationAuthorization, PublicationCandidate, PublicationCapability, PublicationPackage, PublicationRecord, PublicationState, ResearchTopic, RunArtifact, RunError, RunEvent, RunStep, RunTask, TaskType } from './types'

type AnyRecord = Record<string, unknown>

const PLATFORM_LABELS: Record<Platform, string> = {
  douyin: '抖音',
  xiaohongshu: '小红书',
  bilibili: 'B站',
  wechat_channels: '视频号',
  unknown: '未指定',
}

export const PLATFORMS: Exclude<Platform, 'unknown' | 'wechat_channels'>[] = ['douyin', 'xiaohongshu', 'bilibili']

function asRecord(value: unknown): AnyRecord {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as AnyRecord : {}
}

function text(value: unknown): string {
  if (typeof value === 'string') return value.trim()
  return typeof value === 'number' ? String(value) : ''
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function first(value: AnyRecord, ...keys: string[]): unknown {
  for (const key of keys) if (value[key] !== undefined && value[key] !== null) return value[key]
  return undefined
}

function normalizePlatform(value: unknown): Platform {
  const platform = text(value)
  return platform === 'douyin' || platform === 'xiaohongshu' || platform === 'bilibili' || platform === 'wechat_channels'
    ? platform
    : 'unknown'
}

function normalizeType(request: AnyRecord): TaskType {
  if (Object.keys(asRecord(request.offering)).length) return 'Offering'
  if (Object.keys(asRecord(request.atom)).length) return 'ContentAtom'
  return 'unknown'
}

function mapCapability(value: unknown): CapabilityBlock | undefined {
  const record = asRecord(value)
  if (!text(record.code) && !text(record.capability) && !text(record.message)) return undefined
  return {
    code: text(record.code),
    capability: text(record.capability),
    message: text(record.message),
    retryable: Boolean(record.retryable),
  }
}

function mapRunError(value: unknown): RunError | undefined {
  const record = asRecord(value)
  if (!text(record.code) && !text(record.message)) return undefined
  return {
    code: text(record.code),
    message: text(record.message),
    retryable: Boolean(record.retryable),
  }
}

function mapArtifact(value: unknown, index: number): RunArtifact {
  const record = asRecord(value)
  const kind = text(record.kind)
  const path = text(record.path)
  return {
    id: text(record.id) || `artifact-${index}`,
    name: kind || path || `产物 ${index + 1}`,
    kind: kind || undefined,
    path: path || undefined,
    url: text(record.url) || undefined,
    available: Boolean(path || record.url),
  }
}

function eventLevel(type: string): RunEvent['level'] {
  const normalized = type.toLowerCase().replace(/[-\s]/g, '_')
  if (normalized === 'failed' || normalized === 'error') return 'error'
  if (normalized === 'blocked') return 'warning'
  if (['package_ready', 'media_ready', 'distribution_ready'].includes(normalized)) return 'success'
  return 'info'
}

function mapEvent(value: unknown, index: number): RunEvent {
  const record = asRecord(value)
  const type = text(first(record, 'type', 'event_type', 'eventType'))
  const checkpoint = text(first(record, 'checkpoint', 'name', 'message'))
  const payload = asRecord(first(record, 'payload', 'data'))
  return {
    id: text(first(record, 'id', 'event_id')) || `event-${index}`,
    runId: text(first(record, 'run_id', 'runId', 'pipeline_run_id')),
    at: text(first(record, 'created_at', 'createdAt', 'at')),
    type,
    checkpoint,
    payload,
    message: text(first(record, 'message', 'description')) || checkpoint || type || '未命名事件',
    level: eventLevel(type),
  }
}

export function mapEvents(payload: unknown): RunEvent[] {
  return asArray(payload).map(mapEvent)
}

function publicationFor(platform: Platform): PublicationState[] {
  return PLATFORMS.map((item) => ({
    platform: item,
    label: PLATFORM_LABELS[item],
    status: 'not_connected' as const,
    detail: platform === item ? 'publication core 尚未接入' : '当前 Run 契约不包含 publication 记录',
  }))
}

export function extractRunList(payload: unknown): unknown[] {
  return Array.isArray(payload) ? payload : []
}

export function mapRun(rawInput: unknown, index = 0): RunTask {
  const raw = asRecord(rawInput)
  const request = asRecord(raw.request)
  const offering = asRecord(request.offering)
  const atom = asRecord(request.atom)
  const status = normalizeStatus(raw.status)
  const type = normalizeType(request)
  const targets = asArray(request.targets)
  const firstTarget = asRecord(targets[0])
  const platform = normalizePlatform(text(request.platform) || text(firstTarget.platform))
  const capabilityBlock = mapCapability(raw.capability_block)
  const runError = mapRunError(raw.error)
  const title = type === 'Offering'
    ? text(offering.name)
    : type === 'ContentAtom'
      ? text(atom.claim) || text(atom.thesis_id) || text(atom.hook)
      : ''

  return {
    id: text(raw.id) || `unknown-${index}`,
    title: title || '未命名任务',
    type,
    typeLabel: type === 'Offering' ? 'Offering' : type === 'ContentAtom' ? 'ContentAtom' : '未识别',
    platform,
    platformLabel: PLATFORM_LABELS[platform],
    status,
    statusLabel: getStatusLabel(status),
    statusTone: getStatusTone(status),
    // A package-ready run has completed deterministic packaging only. Media
    // production and platform publication remain separate capabilities.
    progress: status === 'package_ready' ? 85 : null,
    progressLabel: status === 'package_ready' ? '产物就绪' : getStatusLabel(status),
    checkpoint: text(raw.checkpoint) || undefined,
    attempt: typeof raw.attempt === 'number' ? raw.attempt : 0,
    createdAt: text(raw.created_at) || undefined,
    updatedAt: text(raw.updated_at) || undefined,
    blockingReason: capabilityBlock?.message || undefined,
    errorMessage: runError?.message || undefined,
    capabilityBlock,
    runError,
    steps: asArray(first(raw, 'steps', 'workflow_steps')).map((value, stepIndex) => mapStep(value, stepIndex)),
    events: [],
    artifacts: asArray(raw.artifacts).map(mapArtifact),
    publications: publicationFor(platform),
    raw: rawInput,
  }
}

function mapStep(value: unknown, index: number): RunStep {
  const record = asRecord(value)
  const status = normalizeStatus(first(record, 'status', 'state'))
  return {
    id: text(first(record, 'id', 'step_id')) || `step-${index}`,
    name: text(first(record, 'name', 'label', 'checkpoint')) || `步骤 ${index + 1}`,
    status,
    statusTone: getStatusTone(status),
    detail: text(first(record, 'detail', 'message', 'description')) || undefined,
    updatedAt: text(first(record, 'updated_at', 'updatedAt')) || undefined,
  }
}

export function mapRuns(payload: unknown): RunTask[] {
  return extractRunList(payload).map(mapRun)
}

function mapPublicationPlatform(value: unknown): Exclude<Platform, 'unknown' | 'wechat_channels'> {
  const platform = normalizePlatform(value)
  return platform === 'douyin' || platform === 'xiaohongshu' || platform === 'bilibili' ? platform : 'douyin'
}

export function mapPublicationCapability(value: unknown): PublicationCapability {
  const record = asRecord(value)
  return {
    platform: mapPublicationPlatform(first(record, 'platform', 'name')),
    configured: Boolean(record.configured),
    reason: text(first(record, 'reason', 'message')) || undefined,
  }
}

export function mapPublicationCapabilities(payload: unknown): PublicationCapability[] {
  const record = asRecord(payload)
  const values = Array.isArray(payload) ? payload : asArray(first(record, 'capabilities', 'data', 'items'))
  const mapped = values.map(mapPublicationCapability)
  const byPlatform = new Map(mapped.map((item) => [item.platform, item]))
  return PLATFORMS.map((platform) => byPlatform.get(platform) || { platform, configured: false, reason: '后端未返回该平台能力' })
}

export function mapPublicationPackage(value: unknown): PublicationPackage {
  const record = asRecord(value)
  const baseCover = text(first(record, 'cover'))
  const baseCoverSha256 = text(first(record, 'cover_sha256', 'coverSha256'))
  const rawCover = text(first(record, 'platform_cover', 'platformCover')) || baseCover
  const rawCoverSha256 = text(first(record, 'platform_cover_sha256', 'platformCoverSha256')) || baseCoverSha256
  const assets = asArray(first(record, 'cover_assets', 'coverAssets')).map(asRecord)
  const assetFor = (...roles: string[]): AnyRecord => assets.find((asset) => roles.includes(text(first(asset, 'role', 'name')).toLowerCase())) || {}
  const generalAsset = assetFor('general', 'generic', 'cover')
  const portraitAsset = assetFor('portrait_3x4', '3:4', '3x4', 'cover_3_4', 'cover34')
  const landscapeAsset = assetFor('landscape_4x3', '4:3', '4x3', 'cover_4_3', 'cover43')
  const cover = text(first(record, 'cover_generic', 'coverGeneric')) || text(first(generalAsset, 'path', 'url')) || baseCover
  const coverSha256 = text(first(record, 'cover_generic_sha256', 'coverGenericSha256')) || text(first(generalAsset, 'sha256', 'hash')) || baseCoverSha256
  const cover34 = text(first(record, 'cover_3_4', 'cover34', 'cover_3x4', 'cover34Path', 'cover_portrait')) || text(first(portraitAsset, 'path', 'url'))
  const cover34Sha256 = text(first(record, 'cover_3_4_sha256', 'cover34Sha256', 'cover_3x4_sha256', 'cover34Hash', 'cover_portrait_sha256')) || text(first(portraitAsset, 'sha256', 'hash'))
  const cover43 = text(first(record, 'cover_4_3', 'cover43', 'cover_4x3', 'cover43Path', 'cover_landscape')) || text(first(landscapeAsset, 'path', 'url'))
  const cover43Sha256 = text(first(record, 'cover_4_3_sha256', 'cover43Sha256', 'cover_4x3_sha256', 'cover43Hash', 'cover_landscape_sha256')) || text(first(landscapeAsset, 'sha256', 'hash'))
  return {
    creator: text(record.creator),
    account: text(record.account),
    platform: mapPublicationPlatform(record.platform),
    run: text(first(record, 'run', 'run_id')),
    atomRevision: text(first(record, 'atom_revision', 'atomRevision')),
    variantId: text(first(record, 'variant_id', 'variantId')),
    video: text(record.video),
    videoSha256: text(first(record, 'video_sha256', 'videoSha256')),
    cover,
    coverSha256,
    cover34,
    cover34Sha256,
    cover43,
    cover43Sha256,
    platformCover: rawCover || cover,
    platformCoverSha256: rawCoverSha256 || coverSha256,
    copy: text(record.copy),
    copySha256: text(first(record, 'copy_sha256', 'copySha256')),
    copySummary: text(first(record, 'copy_summary', 'copySummary')) || undefined,
    title: text(record.title) || undefined,
    tags: asArray(record.tags).map(text).filter(Boolean),
  }
}

export function mapPublicationCandidate(value: unknown): PublicationCandidate {
  const record = asRecord(value)
  const request = asRecord(record.request)
  return {
    request: {
      runId: text(first(request, 'run_id', 'runId')),
      creator: text(request.creator),
      account: text(request.account),
      platform: mapPublicationPlatform(request.platform),
      variantId: text(first(request, 'variant_id', 'variantId')) || undefined,
    },
    package: mapPublicationPackage(record.package),
    fingerprint: text(record.fingerprint),
  }
}

export function mapPublicationAuthorization(value: unknown): PublicationAuthorization {
  const record = asRecord(value)
  return {
    id: text(record.id),
    fingerprint: text(record.fingerprint),
    policyVersion: text(first(record, 'policy_version', 'policyVersion')),
    creator: text(record.creator),
    account: text(record.account),
    platform: mapPublicationPlatform(record.platform),
    run: text(first(record, 'run', 'run_id')),
    variantId: text(first(record, 'variant_id', 'variantId')),
    expiresAt: text(first(record, 'expires_at', 'expiresAt')),
  }
}

function normalizePublicationStatus(value: unknown): PublicationRecord['status'] {
  const status = text(value)
  return status === 'draft_ready' || status === 'committing' || status === 'submitted' || status === 'reviewing' || status === 'published' || status === 'rejected' || status === 'unknown' || status === 'failed'
    ? status
    : 'unknown'
}

export function mapPublication(value: unknown): PublicationRecord {
  const record = asRecord(value)
  return {
    id: text(record.id),
    package: mapPublicationPackage(record.package),
    policyVersion: text(first(record, 'policy_version', 'policyVersion')),
    fingerprint: text(record.fingerprint),
    businessDay: text(first(record, 'business_day', 'businessDay')),
    authorizationId: text(first(record, 'authorization_id', 'authorizationId')),
    status: normalizePublicationStatus(record.status),
    externalId: text(first(record, 'external_id', 'externalId')) || undefined,
    externalUrl: text(first(record, 'external_url', 'externalUrl')) || undefined,
    evidence: text(record.evidence) || undefined,
    lastError: text(first(record, 'last_error', 'lastError')) || undefined,
    createdAt: text(first(record, 'created_at', 'createdAt')) || undefined,
    updatedAt: text(first(record, 'updated_at', 'updatedAt')) || undefined,
  }
}

function mapAnalyticsReview(value: unknown, index: number): AnalyticsReviewSnapshot {
  const record = asRecord(value)
  const status = text(record.status)
  return {
    id: text(record.id) || `review-${index}`,
    provider: text(record.provider),
    platform: text(record.platform),
    status: status === 'submitted' || status === 'reviewing' || status === 'published' || status === 'rejected' ? status : 'unknown',
    externalId: text(record.external_id) || undefined,
    externalUrl: text(record.external_url) || undefined,
    verified: Boolean(record.verified),
    proof: text(record.proof) || undefined,
    observedAt: text(record.observed_at) || undefined,
    lastSeenAt: text(record.last_seen_at) || undefined,
  }
}

function mapAnalyticsMetric(value: unknown, index: number): AnalyticsMetricSnapshot {
  const record = asRecord(value)
  const rawMetrics = asRecord(record.metrics)
  return {
    id: text(record.id) || `metric-${index}`,
    provider: text(record.provider),
    platform: text(record.platform),
    externalId: text(record.external_id),
    metrics: Object.fromEntries(Object.entries(rawMetrics).filter(([, item]) => typeof item === 'number').map(([key, item]) => [key, item as number])),
    observedAt: text(record.observed_at) || undefined,
    lastSeenAt: text(record.last_seen_at) || undefined,
  }
}

function mapAnalyticsJob(value: unknown): AnalyticsSyncJob | undefined {
  const record = asRecord(value)
  if (!text(record.status)) return undefined
  return { status: text(record.status), attempts: typeof record.attempts === 'number' ? record.attempts : 0, lastError: text(record.last_error) || undefined, updatedAt: text(record.updated_at) || undefined, nextAttemptAt: text(record.next_attempt_at) || undefined }
}

function latestByDate<T extends { observedAt?: string; lastSeenAt?: string }>(items: T[]): T | undefined {
  return [...items].sort((a, b) => new Date(b.lastSeenAt || b.observedAt || 0).getTime() - new Date(a.lastSeenAt || a.observedAt || 0).getTime())[0]
}

export function mapPublicationInsights(value: unknown): PublicationAnalytics {
  const record = asRecord(value)
  const reviews = asArray(record.review_snapshots).map(mapAnalyticsReview)
  const metrics = asArray(record.metric_snapshots).map(mapAnalyticsMetric)
  return { latestReview: latestByDate(reviews), latestMetrics: latestByDate(metrics), syncJob: mapAnalyticsJob(record.sync_job) }
}

export function mapAnalyticsCapability(value: unknown): AnalyticsCapability {
  const record = asRecord(value)
  const state = text(record.state)
  return { provider: text(record.provider), platform: text(record.platform), state: state === 'ready' || state === 'blocked' ? state : 'unavailable', reason: text(record.reason) || undefined }
}

export function mapAnalyticsCapabilities(value: unknown): AnalyticsCapability[] {
  return asArray(value).map(mapAnalyticsCapability)
}

export function mapAnalyticsSummary(value: unknown): AnalyticsSummary {
  const record = asRecord(value)
  const jobs = asRecord(record.jobs_by_status)
  return { publicationCount: Number(record.publication_count) || 0, reviewSnapshots: Number(record.review_snapshots) || 0, metricSnapshots: Number(record.metric_snapshots) || 0, events: Number(record.events) || 0, jobsByStatus: Object.fromEntries(Object.entries(jobs).filter(([, item]) => typeof item === 'number').map(([key, item]) => [key, item as number])), lastObservedAt: text(record.last_observed_at) || undefined }
}

export function mapPublications(payload: unknown): PublicationRecord[] {
  const record = asRecord(payload)
  const values = Array.isArray(payload) ? payload : asArray(first(record, 'publications', 'data', 'items'))
  return values.map(mapPublication)
}

export function publicationStatusLabel(status: PublicationRecord['status']): string {
  return {
    draft_ready: '草稿',
    committing: '提交中',
    submitted: '已提交审核',
    reviewing: '审核中',
    published: '已发布',
    rejected: '已拒绝',
    unknown: '待核验',
    failed: '发布失败',
  }[status] || '未知状态'
}

export function publicationStatusTone(status: PublicationRecord['status']): 'green' | 'teal' | 'amber' | 'red' | 'neutral' {
  if (status === 'published' || status === 'submitted' || status === 'reviewing') return status === 'published' ? 'green' : 'teal'
  if (status === 'committing') return 'amber'
  if (status === 'failed' || status === 'rejected') return 'red'
  return 'neutral'
}

export function getRunPublicationAccounts(task: RunTask, platform: Platform): string[] {
  const raw = asRecord(task.raw)
  const request = asRecord(raw.request)
  const targets = asArray(request.targets)
  const accounts = targets
    .map(asRecord)
    .filter((target) => normalizePlatform(target.platform) === platform)
    .map((target) => text(first(target, 'account_id', 'accountId', 'account')))
    .filter(Boolean)
  return [...new Set(accounts)]
}

export function getRunPublicationVariants(task: RunTask): string[] {
  const raw = asRecord(task.raw)
  const request = asRecord(raw.request)
  const values = asArray(first(raw, 'variants', 'content_variants', 'variant_ids') ?? first(request, 'variants', 'content_variants', 'variant_ids'))
  return [...new Set(values.map((value) => {
    const record = asRecord(value)
    return text(first(record, 'id', 'variant_id', 'variantId')) || text(value)
  }).filter(Boolean))]
}

function mapResearchTopic(value: unknown): ResearchTopic {
  const record = asRecord(value)
  const title = text(first(record, 'title', 'name', 'question', 'brief')) || '未命名研究主题'
  return {
    id: text(first(record, 'id', 'topic_id')) || '',
    title,
    question: text(first(record, 'question', 'research_question')) || undefined,
    brief: text(first(record, 'brief', 'description')) || undefined,
    source: text(first(record, 'source', 'source_url')) || undefined,
    raw: value,
  }
}

function mapPipelineAtom(value: unknown, index: number): PipelineAtom {
  const record = asRecord(value)
  return {
    id: text(first(record, 'id', 'atom_id')) || `atom-${index}`,
    title: text(first(record, 'title', 'name', 'claim', 'thesis_id')) || `内容原子 ${index + 1}`,
    status: text(first(record, 'status', 'state')) || undefined,
    raw: value,
  }
}

function mapPipelineChild(value: unknown, index: number): PipelineChildRun {
  const record = asRecord(value)
  const target = asRecord(first(record, 'target', 'distribution_target'))
  const platform = normalizePlatform(first(record, 'platform', 'target_platform') ?? first(target, 'platform', 'target_platform'))
  const status = normalizeStatus(first(record, 'status', 'state'))
  return {
    id: text(first(record, 'id', 'run_id', 'child_run_id')) || `child-${index}`,
    platform,
    platformLabel: PLATFORM_LABELS[platform],
    status,
    statusLabel: getStatusLabel(status),
    statusTone: getStatusTone(status),
    raw: value,
  }
}

export function extractPipelineList(payload: unknown): unknown[] {
  if (Array.isArray(payload)) return payload
  const record = asRecord(payload)
  for (const key of ['data', 'runs', 'pipeline_runs', 'items']) {
    if (Array.isArray(record[key])) return record[key] as unknown[]
  }
  return []
}

export function mapPipelineRun(rawInput: unknown, index = 0): PipelineTask {
  const raw = asRecord(rawInput)
  const request = asRecord(first(raw, 'request', 'pipeline_request'))
  const status = normalizePipelineStatus(raw.status)
  const topicValue = first(raw, 'research_topic', 'researchTopic', 'topic') ?? first(request, 'topic', 'research_topic', 'researchTopic')
  const atoms = asArray(first(raw, 'atoms', 'content_atoms', 'pipeline_atoms')).map(mapPipelineAtom)
  const children = asArray(first(raw, 'children', 'child_runs', 'childRuns')).map(mapPipelineChild)
  const capabilityBlock = mapCapability(first(raw, 'capability_block', 'capabilityBlock'))
  const error = mapRunError(first(raw, 'error', 'run_error'))
  return {
    id: text(raw.id) || `unknown-pipeline-${index}`,
    title: text(first(raw, 'title', 'name')) || mapResearchTopic(topicValue).title,
    status,
    statusLabel: getPipelineStatusLabel(status),
    statusTone: getPipelineStatusTone(status),
    researchTopic: mapResearchTopic(topicValue),
    atoms,
    atomsCount: atoms.length || Number(first(raw, 'atoms_count', 'atom_count')) || 0,
    children,
    capabilityBlock,
    errorMessage: error?.message || text(first(raw, 'error_message', 'errorMessage')) || undefined,
    attempt: typeof raw.attempt === 'number' ? raw.attempt : 0,
    checkpoint: text(first(raw, 'checkpoint', 'current_checkpoint')) || undefined,
    createdAt: text(first(raw, 'created_at', 'createdAt')) || undefined,
    updatedAt: text(first(raw, 'updated_at', 'updatedAt')) || undefined,
    events: [],
    raw: rawInput,
  }
}

export function mapPipelineRuns(payload: unknown): PipelineTask[] {
  return extractPipelineList(payload).map(mapPipelineRun)
}
