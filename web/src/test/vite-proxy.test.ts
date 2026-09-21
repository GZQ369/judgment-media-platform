import { createServer as createHTTPServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createServer as createViteServer } from 'vite'

describe('dev API proxy', () => {
  afterEach(() => vi.unstubAllEnvs())

  it('passes a browser-style loopback POST but forwards foreign origins', async () => {
    const forwarded: Array<{ host: string | undefined, origin: string | undefined }> = []
    const upstream = createHTTPServer((request, response) => {
      forwarded.push({ host: request.headers.host, origin: request.headers.origin })
      response.writeHead(request.headers.origin ? 403 : 204)
      response.end()
    })
    await new Promise<void>((resolve) => upstream.listen(0, '127.0.0.1', resolve))

    const upstreamPort = (upstream.address() as AddressInfo).port
    vi.stubEnv('VITE_DEV_API_TARGET', `http://127.0.0.1:${upstreamPort}`)
    try {
      const vite = await createViteServer({
        configFile: fileURLToPath(new URL('../../vite.config.ts', import.meta.url)),
        logLevel: 'silent',
        server: { host: '127.0.0.1', port: 0 },
      })
      try {
        await vite.listen()
        const viteOrigin = `http://127.0.0.1:${(vite.httpServer!.address() as AddressInfo).port}`
        const post = (origin: string, site: string) => fetch(`${viteOrigin}/api/v1/runs`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Origin: origin, 'Sec-Fetch-Site': site },
          body: '{}',
        })

        expect((await post(viteOrigin, 'same-origin')).status).toBe(204)
        expect(forwarded.at(-1)).toEqual({ host: `127.0.0.1:${upstreamPort}`, origin: undefined })

        expect((await fetch(`${viteOrigin}/api/v1/runs`)).status).toBe(204)
        expect(forwarded.at(-1)?.origin).toBeUndefined()

        expect((await post('https://foreign.example', 'cross-site')).status).toBe(403)
        expect(forwarded.at(-1)?.origin).toBe('https://foreign.example')

        expect((await post(`${viteOrigin}0`, 'same-origin')).status).toBe(403)
        expect(forwarded.at(-1)?.origin).toBe(`${viteOrigin}0`)

        expect((await post(viteOrigin, 'cross-site')).status).toBe(403)
        expect(forwarded.at(-1)?.origin).toBe(viteOrigin)
      } finally {
        await vite.close()
      }
    } finally {
      upstream.closeAllConnections()
      await new Promise<void>((resolve, reject) => upstream.close((error) => error ? reject(error) : resolve()))
    }
  })
})
