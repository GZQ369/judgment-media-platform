import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { mapAnalyticsCapabilities, mapAnalyticsSummary, mapEvents, mapPipelineRun, mapPipelineRuns, mapPublication, mapPublicationCapabilities, mapPublicationCandidate, mapPublicationInsights, mapRun, mapRuns, publicationStatusLabel, publicationStatusTone } from '../mapper'

function fixture(name: string): unknown {
  return JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), 'utf8'))
}

describe('real Go distribution JSON mapper', () => {
  it('maps Run identity from request.offering without publication success inference', () => {
    const task = mapRun(fixture('distribution-run.json'))

    expect(task.id).toBe('run-offering-001')
    expect(task.title).toBe('春季内容诊断')
    expect(task.type).toBe('Offering')
    expect(task.platform).toBe('xiaohongshu')
    expect(task.status).toBe('package_ready')
    expect(task.progress).toBe(85)
    expect(task.progressLabel).toBe('产物就绪')
    expect(task.artifacts[0].path).toBe('runs/run-offering-001/package.zip')
    expect(task.publications.map((item) => item.status)).toEqual(['not_connected', 'not_connected', 'not_connected'])
    expect(task.events).toEqual([])
  })

  it('leaves empty capability and error objects undefined for package_ready runs', () => {
    const task = mapRun({
      ...fixture('distribution-run.json') as Record<string, unknown>,
      capability_block: {},
      error: {},
    })

    expect(task.status).toBe('package_ready')
    expect(task.capabilityBlock).toBeUndefined()
    expect(task.runError).toBeUndefined()
  })

  it('maps cancelled atom Runs and capability blocks from the Go contract', () => {
    const task = mapRun(fixture('distribution-run-research.json'))

    expect(task.type).toBe('ContentAtom')
    expect(task.title).toBe('研究型内容需要模型执行器')
    expect(task.status).toBe('cancelled')
    expect(task.capabilityBlock?.code).toBe('CAPABILITY_UNAVAILABLE')
    expect(task.blockingReason).toContain('no model is connected')
    expect(task.runError?.code).toBe('RUN_CANCELLED')
    expect(task.errorMessage).toContain('run cancelled')
  })

  it('maps the standalone RunEvent response', () => {
    const events = mapEvents(fixture('run-events.json'))

    expect(events).toHaveLength(2)
    expect(events[0]).toMatchObject({
      id: '1',
      runId: 'run-offering-001',
      type: 'checkpoint',
      checkpoint: 'workflow_started',
    })
    expect(events[1].message).toBe('package_ready')
    expect(events[1].payload.artifact_path).toBe('runs/run-offering-001/package.zip')
  })

  it('accepts only the real list response shape', () => {
    expect(mapRuns([fixture('distribution-run.json')])).toHaveLength(1)
    expect(mapRuns({ data: [fixture('distribution-run.json')] })).toEqual([])
  })

  it('maps media_ready without claiming media publication or 100 percent completion', () => {
    const task = mapRun({
      id: 'run-media-001',
      status: 'media_ready',
      request: { offering: { name: '媒体批次' }, platform: 'douyin' },
      artifacts: [],
    })
    expect(task.statusLabel).toBe('媒体已就绪')
    expect(task.progress).toBeNull()
    expect(task.progressLabel).toBe('媒体已就绪')
    expect(task.publications.map((item) => item.status)).toEqual(['not_connected', 'not_connected', 'not_connected'])
  })

  it('maps pipeline parent, atoms, children, and capability blocks', () => {
    const task = mapPipelineRun({
      id: 'pipeline-001',
      status: 'distribution_ready',
      research_topic: { id: 'topic-1', title: '研究主题', question: '问题？' },
      atoms: [{ id: 'atom-1', claim: '结论' }],
      children: [{ id: 'run-1', platform: 'xiaohongshu', status: 'media_ready' }],
      capability_block: { code: 'CAPABILITY_UNAVAILABLE', capability: 'model', message: '需要模型' },
    })
    expect(task.statusLabel).toBe('分发产物已就绪')
    expect(task.atomsCount).toBe(1)
    expect(task.children[0]).toMatchObject({ id: 'run-1', platformLabel: '小红书', status: 'media_ready' })
    expect(task.capabilityBlock?.code).toBe('CAPABILITY_UNAVAILABLE')
    expect(mapPipelineRuns({ pipeline_runs: [{ id: 'pipeline-002', status: 'queued' }] })).toHaveLength(1)
  })

  it('accepts the Go pipeline request.topic and child target.platform shape', () => {
    const task = mapPipelineRun({
      id: 'pipeline-go-001',
      status: 'running',
      request: { topic: { id: 'topic-go-1', title: 'Go 契约主题' } },
      children: [{ id: 'child-go-1', status: 'queued', target: { platform: 'bilibili' } }],
    })
    expect(task.researchTopic).toMatchObject({ id: 'topic-go-1', title: 'Go 契约主题' })
    expect(task.children[0]).toMatchObject({ id: 'child-go-1', platform: 'bilibili', platformLabel: 'B站' })
  })

  it('maps publication capabilities explicitly, including missing platforms', () => {
    expect(mapPublicationCapabilities([{ platform: 'douyin', configured: true }, { platform: 'bilibili', configured: false, reason: 'Ego 未登录' }])).toEqual([
      { platform: 'douyin', configured: true },
      { platform: 'xiaohongshu', configured: false, reason: '后端未返回该平台能力' },
      { platform: 'bilibili', configured: false, reason: 'Ego 未登录' },
    ])
  })

  it('maps analytics readback to latest review, metrics, sync job, and capability', () => {
    const analytics = mapPublicationInsights({
      publication_id: 'pub-1',
      review_snapshots: [
        { id: 'review-old', provider: 'publication', platform: 'douyin', status: 'submitted', observed_at: '2026-09-21T01:00:00Z', last_seen_at: '2026-09-21T01:00:00Z', verified: true },
        { id: 'review-new', provider: 'publication', platform: 'douyin', status: 'unknown', observed_at: '2026-09-21T02:00:00Z', last_seen_at: '2026-09-21T02:00:00Z', verified: false },
      ],
      metric_snapshots: [{ id: 'metric-1', provider: 'douyin-openapi', platform: 'douyin', external_id: 'item-1', metrics: { play_count: 12, like_count: 3 }, last_seen_at: '2026-09-21T02:00:00Z' }],
      sync_job: { status: 'blocked', attempts: 1, last_error: 'no read-only provider', updated_at: '2026-09-21T02:00:00Z' },
    })
    expect(analytics.latestReview).toMatchObject({ id: 'review-new', status: 'unknown', verified: false })
    expect(analytics.latestMetrics?.metrics).toEqual({ play_count: 12, like_count: 3 })
    expect(analytics.syncJob).toMatchObject({ status: 'blocked', lastError: 'no read-only provider' })
    expect(mapAnalyticsCapabilities([{ provider: 'ego', platform: 'bilibili', state: 'unavailable', reason: 'no provider' }])[0]).toMatchObject({ state: 'unavailable' })
    expect(mapAnalyticsSummary({ publication_count: 1, review_snapshots: 2, metric_snapshots: 1, events: 3, jobs_by_status: { blocked: 1 } })).toMatchObject({ publicationCount: 1, metricSnapshots: 1, jobsByStatus: { blocked: 1 } })
  })

  it('maps candidate and durable publication evidence without inferring success', () => {
    const candidate = mapPublicationCandidate({ request: { run_id: 'run-1', creator: 'me', account: 'acct', platform: 'douyin', variant_id: 'v2' }, fingerprint: 'abc', package: { platform: 'douyin', run: 'run-1', account: 'acct', variant_id: 'v2', creator: 'me', video: 'video.mp4', video_sha256: 'video-hash', cover: 'cover.png', cover_sha256: 'cover-hash', cover_3_4: 'cover-34.png', cover_3_4_sha256: 'cover-34-hash', cover_4_3: 'cover-43.png', cover_4_3_sha256: 'cover-43-hash', platform_cover: 'douyin-cover.png', platform_cover_sha256: 'douyin-cover-hash', copy: 'copy', copy_sha256: 'copy-hash', tags: ['codex'] } })
    expect(candidate).toMatchObject({ fingerprint: 'abc', request: { runId: 'run-1', variantId: 'v2' }, package: { video: 'video.mp4', platformCover: 'douyin-cover.png', platformCoverSha256: 'douyin-cover-hash', cover34: 'cover-34.png', cover34Sha256: 'cover-34-hash', cover43: 'cover-43.png', cover43Sha256: 'cover-43-hash', tags: ['codex'] } })
    const boundCovers = mapPublicationCandidate({ request: { run_id: 'run-1', creator: 'me', account: 'acct', platform: 'douyin' }, fingerprint: 'changed-fingerprint', package: { platform: 'douyin', run: 'run-1', account: 'acct', creator: 'me', video: 'video.mp4', video_sha256: 'video-hash', cover: 'selected-platform-cover.png', cover_sha256: 'selected-platform-hash', cover_assets: [{ role: 'general', path: 'general-cover.png', sha256: 'general-hash' }, { role: 'portrait_3x4', path: 'portrait-cover.png', sha256: 'portrait-hash' }, { role: 'landscape_4x3', path: 'landscape-cover.png', sha256: 'landscape-hash' }], copy: 'copy', copy_sha256: 'copy-hash', tags: [] } })
    expect(boundCovers.package).toMatchObject({ platformCover: 'selected-platform-cover.png', platformCoverSha256: 'selected-platform-hash', cover: 'general-cover.png', coverSha256: 'general-hash', cover34: 'portrait-cover.png', cover34Sha256: 'portrait-hash', cover43: 'landscape-cover.png', cover43Sha256: 'landscape-hash' })

    const publication = mapPublication({ id: 'pub-1', status: 'submitted', fingerprint: 'abc', package: { platform: 'douyin', account: 'acct', run: 'run-1', variant_id: 'v2' }, external_id: 'item-1', external_url: 'https://platform.example/item-1', evidence: 'platform status evidence' })
    expect(publication).toMatchObject({ id: 'pub-1', status: 'submitted', externalId: 'item-1', externalUrl: 'https://platform.example/item-1', evidence: 'platform status evidence' })
    expect(publicationStatusLabel('draft_ready')).toBe('草稿')
    expect(publicationStatusLabel('submitted')).toBe('已提交审核')
    expect(publicationStatusLabel('published')).toBe('已发布')
    expect(publicationStatusTone('unknown')).toBe('neutral')
  })
})
