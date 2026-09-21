import type { RunStatus, StatusTone } from './types'

const STATUS_LABELS: Record<RunStatus, string> = {
  queued: '排队中',
  running: '执行中',
  blocked: '已阻塞',
  package_ready: '产物已就绪',
  media_ready: '媒体已就绪',
  failed: '失败',
  cancelled: '已取消',
  unknown: '未知状态',
}

const STATUS_TONES: Record<RunStatus, StatusTone> = {
  queued: 'neutral',
  running: 'teal',
  blocked: 'amber',
  package_ready: 'green',
  media_ready: 'green',
  failed: 'red',
  cancelled: 'neutral',
  unknown: 'neutral',
}

export function normalizeStatus(value: unknown): RunStatus {
  const status = String(value ?? '').trim().toLowerCase().replace(/[-\s]/g, '_')
  if (['queued', 'pending', 'created', 'waiting'].includes(status)) return 'queued'
  if (['running', 'processing', 'in_progress', 'executing'].includes(status)) return 'running'
  if (['blocked', 'needs_input', 'action_required'].includes(status)) return 'blocked'
  if (status === 'package_ready') return 'package_ready'
  if (status === 'media_ready') return 'media_ready'
  if (['failed', 'failure', 'error'].includes(status)) return 'failed'
  if (status === 'cancelled') return 'cancelled'
  return 'unknown'
}

import type { PipelineStatus } from './types'

const PIPELINE_STATUS_LABELS: Record<PipelineStatus, string> = {
  queued: '排队中',
  running: '执行中',
  blocked: '已阻塞',
  failed: '失败',
  distribution_ready: '分发产物已就绪',
  cancelled: '已取消',
  unknown: '未知状态',
}

const PIPELINE_STATUS_TONES: Record<PipelineStatus, StatusTone> = {
  queued: 'neutral',
  running: 'teal',
  blocked: 'amber',
  failed: 'red',
  distribution_ready: 'green',
  cancelled: 'neutral',
  unknown: 'neutral',
}

export function normalizePipelineStatus(value: unknown): PipelineStatus {
  const status = String(value ?? '').trim().toLowerCase().replace(/[-\s]/g, '_')
  if (['queued', 'pending', 'created', 'waiting'].includes(status)) return 'queued'
  if (['running', 'processing', 'in_progress', 'executing'].includes(status)) return 'running'
  if (['blocked', 'needs_input', 'action_required'].includes(status)) return 'blocked'
  if (status === 'distribution_ready') return 'distribution_ready'
  if (['failed', 'failure', 'error'].includes(status)) return 'failed'
  if (status === 'cancelled') return 'cancelled'
  return 'unknown'
}

export function getPipelineStatusLabel(status: PipelineStatus): string {
  return PIPELINE_STATUS_LABELS[status]
}

export function getPipelineStatusTone(status: PipelineStatus): StatusTone {
  return PIPELINE_STATUS_TONES[status]
}

export function getStatusLabel(status: RunStatus): string {
  return STATUS_LABELS[status]
}

export function getStatusTone(status: RunStatus): StatusTone {
  return STATUS_TONES[status]
}

export function canCancel(status: RunStatus): boolean {
  return ['queued', 'running', 'blocked'].includes(status)
}

export function canResume(status: RunStatus): boolean {
  return ['cancelled', 'blocked'].includes(status)
}

export function canRetry(status: RunStatus): boolean {
  return ['failed', 'cancelled'].includes(status)
}
