import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The build lands inside the admin package so go:embed can pick it up;
// committing dist keeps `go build ./...` working on machines without node.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/admin/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8090',
    },
  },
})
