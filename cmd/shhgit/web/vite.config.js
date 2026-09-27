import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The production bundle is embedded into the shhgit binary with go:embed and
// served from the same origin as the API, so no proxy is involved there.
//
// `npm run dev` is the only mode that needs a proxy. shhgit's API guards itself
// with a same-origin check (`Origin` must equal `Host`) and a loopback Host
// check, so the proxy MUST forward the browser's original Host header
// (localhost:5173) instead of rewriting it to the backend (:8080). With
// `changeOrigin: true` the forwarded Origin (localhost:5173) and Host
// (localhost:8080) disagree and every API call is answered 403.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    rollupOptions: {
      output: {
        // Recharts is by far the largest dependency; splitting the vendor
        // libraries keeps the app chunk small and lets the hashed vendor files
        // stay cached across UI releases.
        manualChunks: {
          react: ['react', 'react-dom'],
          charts: ['recharts'],
          icons: ['lucide-react'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false },
      '/health': { target: 'http://127.0.0.1:8080', changeOrigin: false },
    },
  },
})
