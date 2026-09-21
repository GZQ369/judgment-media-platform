import { fileURLToPath, URL } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const apiTarget = env.VITE_DEV_API_TARGET || 'http://127.0.0.1:8088'

  return {
    plugins: [vue()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      proxy: {
        '/api': {
          target: apiTarget,
          changeOrigin: true,
          configure(proxy) {
            proxy.on('proxyReq', (proxyRequest, request) => {
              const host = request.headers.host
              const origin = request.headers.origin
              const site = request.headers['sec-fetch-site']
              const remoteAddress = request.socket.remoteAddress
              if (
                host &&
                /^(?:localhost|127\.0\.0\.1|\[::1\]):\d+$/i.test(host) &&
                origin === `http://${host}` &&
                (site === undefined || site === 'same-origin') &&
                (remoteAddress === '127.0.0.1' || remoteAddress === '::1' || remoteAddress === '::ffff:127.0.0.1')
              ) {
                proxyRequest.removeHeader('Origin')
              }
            })
          },
        },
      },
    },
  }
})
