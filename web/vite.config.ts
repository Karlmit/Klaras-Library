import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    // Emitted into web/dist, which the Go binary embeds via go:embed.
    outDir: 'dist',
    emptyOutDir: true,
    // Everything is served from one origin with a strict CSP; source maps are
    // useful and cost nothing to a self-hosted deployment.
    sourcemap: true,
  },
  server: {
    port: 5173,
    // `npm run dev` talks to the Go server rather than mocking it.
    proxy: { '/api': 'http://127.0.0.1:8083' },
  },
})
