import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  build: {
    // Embedded into the Go binary by internal/web (go:embed).
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    // internal/web reads the manifest to know which files the sign-in page needs: only those
    // are served to visitors without a session.
    manifest: true,
    rollupOptions: {
      input: {
        index: fileURLToPath(new URL('./index.html', import.meta.url)),
        login: fileURLToPath(new URL('./login.html', import.meta.url)),
      },
    },
  },
  server: {
    // `npm run dev` talks to a locally running `go run ./cmd/runwarden`.
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/auth': { target: 'http://localhost:8080' },
    },
  },
})
