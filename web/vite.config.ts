import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  build: {
    // Embedded into the Go binary by internal/web (go:embed).
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` talks to a locally running `go run ./cmd/runwarden`.
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
    },
  },
})
