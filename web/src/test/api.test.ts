import { afterEach, describe, expect, it, vi } from 'vitest'
import { authorizePublication, cancelRun, commitPublication, createPublicationCandidate, createRun, getAnalyticsCapabilities, getAnalyticsSummary, getPipelineRun, getPipelineRunEvents, getPipelineRuns, getPublicationCapabilities, getPublicationInsights, getPublications, preparePublication, reconcilePublication, syncPublicationInsights } from '../api'

describe('distribution API requests', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('sends the real offering/atom request shapes', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}', { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await createRun({ type: 'Offering', name: '服务', platform: 'xiaohongshu', brief: '说明', offeringKind: 'service' })
    const offeringBody = JSON.parse(String(fetchMock.mock.calls[0][1]?.body)) as Record<string, unknown>
    expect(offeringBody).toEqual({
      offering: { kind: 'service', name: '服务', description: '说明' },
      platform: 'xiaohongshu',
    })

    await createRun({ type: 'ContentAtom', name: '研究题目', platform: 'douyin', brief: '研究结论', offeringKind: 'product' })
    const atomBody = JSON.parse(String(fetchMock.mock.calls[1][1]?.body)) as Record<string, any>
    expect(atomBody.platform).toBe('douyin')
    expect(atomBody.atom).toMatchObject({ kind: 'research', thesis_id: '研究题目', claim: '研究结论' })
    expect(atomBody.type).toBeUndefined()
    expect(atomBody.brief).toBeUndefined()
  })

  it('sends JSON for action POSTs', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await cancelRun('run-1')
    const init = fetchMock.mock.calls[0][1]
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/json')
    expect(init?.body).toBe('{}')
  })

  it('uses the pipeline list, detail, and events GET endpoints', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await getPipelineRuns()
    await getPipelineRun('pipeline/1')
    await getPipelineRunEvents('pipeline/1', 42)

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      '/api/v1/pipeline-runs',
      '/api/v1/pipeline-runs/pipeline%2F1',
      '/api/v1/pipeline-runs/pipeline%2F1/events?after_id=42',
    ])
    expect(fetchMock.mock.calls.every((call) => (call[1]?.method || 'GET') === 'GET')).toBe(true)
  })

  it('keeps publication endpoints strict and uses the durable transaction fields', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const candidate = { runId: 'run-1', creator: 'creator', account: 'account-1', platform: 'douyin' as const, variantId: 'v1' }
    const policy = { version: 'v1', selected: ['douyin' as const] }

    const options = { token: 'page-secret' }
    await getPublications(25, options)
    await getPublicationCapabilities(options)
    await createPublicationCandidate(candidate, options)
    await authorizePublication({ candidate, confirm: true, policy, expiresAt: '2026-09-21T01:00:00Z' }, options)
    await preparePublication({ candidate, authorizationId: 'auth-1', fingerprint: 'fp-1', policy }, options)
    await commitPublication('pub-1', 'fp-1', options)
    await reconcilePublication('pub-1', options)
    await getPublicationInsights('pub-1', options)
    await syncPublicationInsights('pub-1', options)
    await getAnalyticsCapabilities(options)
    await getAnalyticsSummary(options)

    expect(fetchMock.mock.calls.map((call) => String(call[0]))).toEqual([
      '/api/v1/publications?limit=25',
      '/api/v1/publications/capabilities',
      '/api/v1/publications/candidate',
      '/api/v1/publications/authorize',
      '/api/v1/publications/prepare',
      '/api/v1/publications/pub-1/commit',
      '/api/v1/publications/pub-1/reconcile',
      '/api/v1/publications/pub-1/insights',
      '/api/v1/publications/pub-1/sync',
      '/api/v1/analytics/capabilities',
      '/api/v1/analytics/summary',
    ])
    const authorizeBody = JSON.parse(String(fetchMock.mock.calls[3][1]?.body))
    expect(authorizeBody).toEqual({ candidate: { run_id: 'run-1', creator: 'creator', account: 'account-1', platform: 'douyin', variant_id: 'v1' }, confirm: true, policy, expires_at: '2026-09-21T01:00:00Z' })
    expect(JSON.parse(String(fetchMock.mock.calls[5][1]?.body))).toEqual({ fingerprint: 'fp-1', confirm: true })
    expect(fetchMock.mock.calls.every((call) => new Headers(call[1]?.headers).get('Authorization') === 'Bearer page-secret')).toBe(true)
    expect(JSON.parse(String(fetchMock.mock.calls[6][1]?.body))).toEqual({})
  })

  it('does not send publication requests without a token', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(getPublications(25, { token: '' })).rejects.toThrow('服务端令牌未提供')
    await expect(commitPublication('pub-1', 'fp-1', { token: '  ' })).rejects.toThrow('服务端令牌未提供')
    await expect(getPublicationInsights('pub-1', { token: '' })).rejects.toThrow('服务端令牌未提供')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
