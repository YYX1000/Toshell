import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// 开发环境代理目标：默认指向本地服务端的管理 API，开箱即用。
// 需要指向别的服务端时用环境变量覆盖，例如 PowerShell:
//   $env:VITE_PROXY_TARGET="http://192.168.1.28:18081"; npm run dev
//
// 端口必须写 server.api_port（默认 18081）—— 服务端**不监听** server.port，
// 配置里那两个端口不是一回事；写成 8081 之类的默认值会让 /api 代理全部超时。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const proxyTarget = env.VITE_PROXY_TARGET || 'http://localhost:18081'

  return {
    plugins: [react()],
    resolve: {
      alias: {
        '@': '/src',
      },
    },
    server: {
      port: 3002,
      host: '0.0.0.0',
      proxy: {
        '/api': {
          target: proxyTarget,
          changeOrigin: true,
          ws: true,
          configure: (proxy, _options) => {
            proxy.on('error', (err, _req, _res) => {
              console.log('proxy error', err)
            })
            proxy.on('proxyReqWs', (proxyReq, req, _socket) => {
              console.log('WebSocket proxy request:', req.url)
            })
          },
        },
      },
    },
    build: {
      // 分包：把体积大的第三方依赖拆成独立 chunk，改善首屏加载与缓存命中，
      // 消除单 bundle 超过 500KB 的警告。
      rollupOptions: {
        output: {
          manualChunks: {
            react: ['react', 'react-dom', 'react-router-dom', 'zustand'],
            charts: ['recharts'],
            xterm: ['@xterm/xterm', '@xterm/addon-fit'],
            vendor: ['axios', 'date-fns', 'lucide-react'],
          },
        },
      },
    },
  }
})
