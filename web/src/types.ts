export type TaskType = 'Offering' | 'ContentAtom' | 'unknown'
export type Platform = 'douyin' | 'xiaohongshu' | 'bilibili' | 'wechat_channels' | 'unknown'
export type StatusTone = 'teal' | 'green' | 'red' | 'amber' | 'neutral'
export type RunStatus =
  | 'queued'
  | 'running'
  | 'blocked'
  | 'package_ready'
  | 'media_ready'
  | 'failed'
  | 'cancelled'
  | 'unknown'

export interface RunStep {
  id: string
  name: string
  status: RunStatus
  statusTone: StatusTone
  detail?: string
  updatedAt?: string
}

export interface RunEvent {
  id: string
  runId: string
  at: string
  type: string
  checkpoint: string
  payload: Record<string, unknown>
  message: string
  level: 'info' | 'warning' | 'error' | 'success'
}

export interface RunArtifact {
  id: string
  name: string
  kind?: string
  path?: string
  url?: string
  available: boolean
}

export interface PublicationState {
  platform: Platform
  label: string
  status: 'not_connected' | 'not_submitted' | 'published' | 'reviewing' | 'submitted' | 'action_required' | 'failed' | 'unknown'
  detail?: string
  url?: string
}

export type PublicationStatus =
  | 'draft_ready'
  | 'committing'
  | 'submitted'
  | 'reviewing'
  | 'published'
  | 'rejected'
  | 'unknown'
  | 'failed'

export interface PublicationCapability {
  platform: Exclude<Platform, 'unknown' | 'wechat_channels'>
  configured: boolean
  reason?: string
}

export interface PublicationPackage {
  creator: string
  account: string
  platform: Exclude<Platform, 'unknown' | 'wechat_channels'>
  run: string
  atomRevision: string
  variantId: string
  video: string
  videoSha256: string
  cover: string
  coverSha256: string
  cover34: string
  cover34Sha256: string
  cover43: string
  cover43Sha256: string
  platformCover: string
  platformCoverSha256: string
  copy: string
  copySha256: string
  copySummary?: string
  title?: string
  tags: string[]
}

export interface PublicationCandidate {
  request: PublicationCandidateRequest
  package: PublicationPackage
  fingerprint: string
}

export interface PublicationCandidateRequest {
  runId: string
  creator: string
  account: string
  platform: Exclude<Platform, 'unknown' | 'wechat_channels'>
  variantId?: string
}

export interface PublicationPolicy {
  version: string
  selected: Array<Exclude<Platform, 'unknown' | 'wechat_channels'>>
}

export interface PublicationAuthorization {
  id: string
  fingerprint: string
  policyVersion: string
  creator: string
  account: string
  platform: Exclude<Platform, 'unknown' | 'wechat_channels'>
  run: string
  variantId: string
  expiresAt: string
}

export interface PublicationRecord {
  id: string
  package: PublicationPackage
  policyVersion: string
  fingerprint: string
  businessDay: string
  authorizationId: string
  status: PublicationStatus
  externalId?: string
  externalUrl?: string
  evidence?: string
  lastError?: string
  createdAt?: string
  updatedAt?: string
  analytics?: PublicationAnalytics
}

export type AnalyticsReviewStatus = 'submitted' | 'reviewing' | 'published' | 'rejected' | 'unknown'

export interface AnalyticsReviewSnapshot {
  id: string
  provider: string
  platform: string
  status: AnalyticsReviewStatus
  externalId?: string
  externalUrl?: string
  verified: boolean
  proof?: string
  observedAt?: string
  lastSeenAt?: string
}

export interface AnalyticsMetricSnapshot {
  id: string
  provider: string
  platform: string
  externalId: string
  metrics: Record<string, number>
  observedAt?: string
  lastSeenAt?: string
}

export interface AnalyticsSyncJob {
  status: string
  attempts: number
  lastError?: string
  updatedAt?: string
  nextAttemptAt?: string
}

export interface AnalyticsCapability {
  provider: string
  platform: string
  state: 'ready' | 'blocked' | 'unavailable'
  reason?: string
}

export interface AnalyticsSummary {
  publicationCount: number
  reviewSnapshots: number
  metricSnapshots: number
  events: number
  jobsByStatus: Record<string, number>
  lastObservedAt?: string
}

export interface PublicationAnalytics {
  latestReview?: AnalyticsReviewSnapshot
  latestMetrics?: AnalyticsMetricSnapshot
  syncJob?: AnalyticsSyncJob
  capability?: AnalyticsCapability
}

export interface CapabilityBlock {
  code: string
  capability: string
  message: string
  retryable: boolean
}

export interface RunError {
  code: string
  message: string
  retryable: boolean
}

export interface RunTask {
  id: string
  title: string
  type: TaskType
  typeLabel: string
  platform: Platform
  platformLabel: string
  status: RunStatus
  statusLabel: string
  statusTone: StatusTone
  progress: number | null
  progressLabel: string
  checkpoint?: string
  createdAt?: string
  updatedAt?: string
  attempt: number
  blockingReason?: string
  errorMessage?: string
  capabilityBlock?: CapabilityBlock
  runError?: RunError
  steps: RunStep[]
  events: RunEvent[]
  artifacts: RunArtifact[]
  publications: PublicationState[]
  raw: unknown
}

export type PipelineStatus = 'queued' | 'running' | 'blocked' | 'failed' | 'distribution_ready' | 'cancelled' | 'unknown'

export interface ResearchTopic {
  id: string
  title: string
  question?: string
  brief?: string
  source?: string
  raw: unknown
}

export interface PipelineAtom {
  id: string
  title: string
  status?: string
  raw: unknown
}

export interface PipelineChildRun {
  id: string
  platform: Platform
  platformLabel: string
  status: RunStatus
  statusLabel: string
  statusTone: StatusTone
  raw: unknown
}

export interface PipelineTask {
  id: string
  title: string
  status: PipelineStatus
  statusLabel: string
  statusTone: StatusTone
  researchTopic: ResearchTopic
  atoms: PipelineAtom[]
  atomsCount: number
  children: PipelineChildRun[]
  capabilityBlock?: CapabilityBlock
  errorMessage?: string
  attempt: number
  checkpoint?: string
  createdAt?: string
  updatedAt?: string
  events: RunEvent[]
  raw: unknown
}

export type PipelineDetail = PipelineTask

export interface CreateRunInput {
  type: 'Offering' | 'ContentAtom'
  name: string
  platform: Exclude<Platform, 'unknown'>
  brief: string
  offeringKind: 'product' | 'service' | 'capability'
}
