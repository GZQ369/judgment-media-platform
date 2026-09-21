import { describe, expect, it } from 'vitest'
import { canCancel, canResume, canRetry, getPipelineStatusLabel, getStatusLabel, normalizePipelineStatus, normalizeStatus } from '../status'

describe('run status helpers', () => {
  it('normalizes backend variants', () => {
    expect(normalizeStatus('running')).toBe('running')
    expect(normalizeStatus('cancelled')).toBe('cancelled')
    expect(normalizeStatus('something_new')).toBe('unknown')
    expect(getStatusLabel('blocked')).toBe('已阻塞')
    expect(normalizeStatus('media-ready')).toBe('media_ready')
    expect(getStatusLabel('media_ready')).toBe('媒体已就绪')
  })

  it('limits actions to states the backend can transition', () => {
    expect(canCancel('running')).toBe(true)
    expect(canResume('blocked')).toBe(true)
    expect(canRetry('failed')).toBe(true)
    expect(canRetry('package_ready')).toBe(false)
  })

  it('keeps pipeline parent statuses separate from distribution run statuses', () => {
    expect(normalizePipelineStatus('distribution_ready')).toBe('distribution_ready')
    expect(getPipelineStatusLabel('distribution_ready')).toBe('分发产物已就绪')
    expect(normalizePipelineStatus('package_ready')).toBe('unknown')
  })
})
