import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// `npm run dev` serves the UI on :5173 and proxies the API to the Go server on :8080.
// The production CSP forbids inline scripts and styles: keep every asset a separate file.
export default defineConfig({
  plugins: [react()],
  build: { assetsInlineLimit: 0, cssCodeSplit: false },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
  test: { include: ['src/**/*.test.ts'] },
})
