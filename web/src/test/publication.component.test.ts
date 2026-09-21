// @vitest-environment jsdom
import { createApp, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../App.vue'

const mediaRun = {
  id: 'run-media-1',
  status: 'media_ready',
  request: { offering: { name: '媒体内容' }, platform: 'douyin', targets: [{ platform: 'douyin', account_id: 'acct-1' }] },
  artifacts: [{ id: 'rendered-video', kind: 'video', path: 'video.mp4' }, { id: 'cover', kind: 'cover', path: 'cover.png' }],
}

function responseFor(url: string): unknown {
  if (url.endsWith('/api/v1/runs')) return [mediaRun]
  if (url.endsWith('/api/v1/publications?limit=50')) return []
  if (url.endsWith('/api/v1/publications/capabilities')) return [{ platform: 'douyin', configured: false, reason: '未配置访问令牌' }, { platform: 'xiaohongshu', configured: false, reason: '未登录' }, { platform: 'bilibili', configured: false, reason: '未登录' }]
  if (url.endsWith('/api/v1/runs/run-media-1')) return mediaRun
  if (url.includes('/events')) return []
  return {}
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

describe('publication board', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('renders missing capabilities and opens candidate flow without publishing', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(responseFor(String(input))), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const host = document.createElement('div')
    document.body.appendChild(host)
    const app = createApp(App)
    app.mount(host)
    await settle()

    const nav = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('发布状态')) as HTMLButtonElement
    nav.click()
    await settle()
    const tokenInput = host.querySelector('input[type="password"]') as HTMLInputElement
    expect(tokenInput).toBeTruthy()
    tokenInput.value = 'page-secret'
    tokenInput.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    const readButton = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('读取发布状态')) as HTMLButtonElement
    readButton.click()
    await settle()
    expect(host.textContent).toContain('未配置访问令牌')
    expect(host.textContent).toContain('暂无发布记录')
    const create = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('从媒体 Run 创建候选')) as HTMLButtonElement
    expect(create.disabled).toBe(false)
    create.click()
    await settle()
    expect(host.textContent).toContain('创建发布候选')
    expect(host.textContent).toContain('仅允许选择 media_ready Run')
    expect(host.textContent).not.toContain('确认最终提交')
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/commit'))).toBe(false)
    expect(localStorage.getItem('publicationToken')).toBeNull()
    expect(sessionStorage.getItem('publicationToken')).toBeNull()
    app.unmount()
  })

  it('keeps publication actions disabled and sends no publication request without a token', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(responseFor(String(input))), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const host = document.createElement('div')
    document.body.appendChild(host)
    const app = createApp(App)
    app.mount(host)
    await settle()

    const nav = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('发布状态')) as HTMLButtonElement
    nav.click()
    await settle()
    expect(host.textContent).toContain('服务端令牌未提供')
    const create = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('从媒体 Run 创建候选')) as HTMLButtonElement
    expect(create.disabled).toBe(true)
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/v1/publications'))).toBe(false)
    app.unmount()
  })

  it('requires the independent final confirmation before commit', async () => {
    const draftPublication = {
      id: 'pub-1',
      status: 'draft_ready',
      fingerprint: 'fingerprint-1',
      package: { platform: 'douyin', account: 'acct-1', run: 'run-media-1', variant_id: 'v1', creator: 'creator', video: 'video.mp4', video_sha256: 'video-hash', cover: 'cover.png', cover_sha256: 'cover-hash', cover_3_4: 'cover-34.png', cover_3_4_sha256: 'cover-34-hash', cover_4_3: 'cover-43.png', cover_4_3_sha256: 'cover-43-hash', copy: 'copy', copy_sha256: 'copy-hash', tags: [] },
    }
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/api/v1/runs')) return new Response(JSON.stringify([mediaRun]), { status: 200 })
      if (url.endsWith('/api/v1/publications?limit=50')) return new Response(JSON.stringify([draftPublication]), { status: 200 })
      if (url.endsWith('/api/v1/publications/capabilities')) return new Response(JSON.stringify([]), { status: 200 })
      if (url.endsWith('/api/v1/publications/pub-1')) return new Response(JSON.stringify(draftPublication), { status: 200 })
      return new Response('{}', { status: 200 })
    })
    vi.stubGlobal('fetch', fetchMock)
    const host = document.createElement('div')
    document.body.appendChild(host)
    const app = createApp(App)
    app.mount(host)
    await settle()

    ;[...host.querySelectorAll('button')].find((button) => button.textContent?.includes('发布状态'))?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await settle()
    const tokenInput = host.querySelector('input[type="password"]') as HTMLInputElement
    tokenInput.value = 'page-secret'
    tokenInput.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    ;[...host.querySelectorAll('button')].find((button) => button.textContent?.includes('读取发布状态'))?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await settle()
    ;(host.querySelector('.publication-row-grid') as HTMLButtonElement).click()
    await settle()
    ;[...host.querySelectorAll('button')].find((button) => button.textContent?.includes('准备最终提交'))?.click()
    await settle()

    const commitButton = [...host.querySelectorAll('button')].find((button) => button.textContent?.includes('确认最终提交')) as HTMLButtonElement
    expect(commitButton.disabled).toBe(true)
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes('/commit'))).toBe(false)

    const confirmation = [...host.querySelectorAll('input[type="checkbox"]')].at(-1) as HTMLInputElement
    confirmation.click()
    await settle()
    expect(commitButton.disabled).toBe(false)
    commitButton.click()
    await settle()
    const commitCall = fetchMock.mock.calls.find((call) => String(call[0]).includes('/commit'))
    expect(commitCall).toBeTruthy()
    expect(JSON.parse(String(commitCall?.[1]?.body))).toEqual({ fingerprint: 'fingerprint-1', confirm: true })
    app.unmount()
  })
})
