import type {
  CreateRunInput,
  PublicationAuthorization,
  PublicationCandidate,
  PublicationCandidateRequest,
  PublicationCapability,
  PublicationPolicy,
  PublicationRecord,
} from './types'

const API_BASE = (import.meta.env.VITE_API_BASE || '/api').replace(/\/$/, '')

export class ApiError extends Error {
  status: number
  body: unknown

  constructor(message: string, status: number, body: unknown) {
    super(message)
    this.name = 'ApiError'
    this.body = body
    this.status = status
  }
}

export interface PublicationRequestOptions {
  token: string
  signal?: AbortSignal
}

async function request(path: string, init: RequestInit = {}): Promise<unknown> {
  const method = (init.method || 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (method === 'POST' && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  let response: Response
  try {
    response = await fetch(API_BASE + path, { ...init, headers })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    const detail = error instanceof Error && error.message ? `（${error.message}）` : ''
    throw new ApiError(`无法连接服务端，可能是 CORS 或网络错误；未收到服务端响应${detail}`, 0, error)
  }
  const text = await response.text()
  let body: unknown
  try {
    body = text ? JSON.parse(text) : undefined
  } catch {
    body = text
  }
  if (!response.ok) {
    const record = body && typeof body === 'object' && !Array.isArray(body) ? body as Record<string, unknown> : {}
    const error = record.error
    const message = error && typeof error === 'object' && 'message' in error
      ? String((error as { message: unknown }).message)
      : typeof error === 'string'
        ? error
        : typeof record.message === 'string'
          ? record.message
          : typeof record.detail === 'string'
            ? record.detail
            : 'API 请求失败（' + response.status + '）'
    const statusMessage = response.status === 401
      ? `服务端令牌无效或已过期（401）：${message}`
      : response.status === 403
        ? `服务端拒绝此操作（403）：${message}`
        : message
    throw new ApiError(statusMessage, response.status, body)
  }
  return body
}

export function getRuns(): Promise<unknown> {
  return request('/v1/runs')
}

export function getRun(id: string, signal?: AbortSignal): Promise<unknown> {
  return request('/v1/runs/' + encodeURIComponent(id), { signal })
}

export function getRunEvents(id: string, afterId = 0, signal?: AbortSignal): Promise<unknown> {
  return request('/v1/runs/' + encodeURIComponent(id) + '/events?after_id=' + afterId, { signal })
}

export function createRun(input: CreateRunInput): Promise<unknown> {
  const body = input.type === 'Offering'
    ? {
        offering: { kind: input.offeringKind, name: input.name.trim(), description: input.brief.trim() },
        platform: input.platform,
      }
    : {
        atom: {
          kind: 'research',
          schema_version: '1.0',
          revision: 1,
          source_type: 'web',
          source_id: 'web-ui',
          thesis_id: input.name.trim(),
          audience: '',
          angle: '',
          hook: '',
          claim: input.brief.trim() || input.name.trim(),
          evidence_ids: [],
          counter_point: '',
          insight: '',
          desired_action: '',
          cta: '',
          priority: 0,
        },
        platform: input.platform,
      }
  return request('/v1/runs', { method: 'POST', body: JSON.stringify(body) })
}

function postAction(id: string, action: string): Promise<unknown> {
  return request('/v1/runs/' + encodeURIComponent(id) + '/' + action, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  })
}

export function cancelRun(id: string): Promise<unknown> {
  return postAction(id, 'cancel')
}

export function resumeRun(id: string): Promise<unknown> {
  return postAction(id, 'resume')
}

export function retryRun(id: string): Promise<unknown> {
  return postAction(id, 'retry')
}

export function getPipelineRuns(signal?: AbortSignal): Promise<unknown> {
  return request('/v1/pipeline-runs', { signal })
}

export function getPipelineRun(id: string, signal?: AbortSignal): Promise<unknown> {
  return request('/v1/pipeline-runs/' + encodeURIComponent(id), { signal })
}

export function getPipelineRunEvents(id: string, afterId = 0, signal?: AbortSignal): Promise<unknown> {
  return request('/v1/pipeline-runs/' + encodeURIComponent(id) + '/events?after_id=' + encodeURIComponent(String(afterId)), { signal })
}

function publicationRequest(path: string, options: PublicationRequestOptions, init: RequestInit = {}): Promise<unknown> {
  const token = options.token.trim()
  if (!token) return Promise.reject(new ApiError('服务端令牌未提供', 0, undefined))
  const headers = new Headers(init.headers)
  headers.set('Authorization', `Bearer ${token}`)
  return request(path, { ...init, headers, signal: options.signal })
}

export function getPublications(limit = 50, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications?limit=' + encodeURIComponent(String(limit)), options)
}

export function getPublicationCapabilities(options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/capabilities', options)
}

export function createPublicationCandidate(input: PublicationCandidateRequest, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/candidate', options, {
    method: 'POST',
    body: JSON.stringify({
      run_id: input.runId,
      creator: input.creator,
      account: input.account,
      platform: input.platform,
      ...(input.variantId ? { variant_id: input.variantId } : {}),
    }),
  })
}

export function authorizePublication(input: {
  candidate: PublicationCandidateRequest
  confirm: boolean
  policy: PublicationPolicy
  expiresAt: string
}, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/authorize', options, {
    method: 'POST',
    body: JSON.stringify({
      candidate: toCandidateRequest(input.candidate),
      confirm: input.confirm,
      policy: input.policy,
      expires_at: input.expiresAt,
    }),
  })
}

export function preparePublication(input: {
  candidate: PublicationCandidateRequest
  authorizationId: string
  fingerprint: string
  policy: PublicationPolicy
  businessDay?: string
}, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/prepare', options, {
    method: 'POST',
    body: JSON.stringify({
      candidate: toCandidateRequest(input.candidate),
      authorization_id: input.authorizationId,
      fingerprint: input.fingerprint,
      policy: input.policy,
      ...(input.businessDay ? { business_day: input.businessDay } : {}),
    }),
  })
}

export function getPublication(id: string, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/' + encodeURIComponent(id), options)
}

export function commitPublication(id: string, fingerprint: string, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/' + encodeURIComponent(id) + '/commit', options, {
    method: 'POST',
    body: JSON.stringify({ fingerprint, confirm: true }),
  })
}

export function reconcilePublication(id: string, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/' + encodeURIComponent(id) + '/reconcile', options, {
    method: 'POST',
    body: JSON.stringify({}),
  })
}

export function getPublicationInsights(id: string, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/' + encodeURIComponent(id) + '/insights', options)
}

export function syncPublicationInsights(id: string, options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/publications/' + encodeURIComponent(id) + '/sync', options, {
    method: 'POST',
    body: JSON.stringify({}),
  })
}

export function getAnalyticsCapabilities(options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/analytics/capabilities', options)
}

export function getAnalyticsSummary(options: PublicationRequestOptions): Promise<unknown> {
  return publicationRequest('/v1/analytics/summary', options)
}

function toCandidateRequest(input: PublicationCandidateRequest): Record<string, unknown> {
  return {
    run_id: input.runId,
    creator: input.creator,
    account: input.account,
    platform: input.platform,
    ...(input.variantId ? { variant_id: input.variantId } : {}),
  }
}

export type { PublicationAuthorization, PublicationCandidate, PublicationCapability, PublicationRecord }
