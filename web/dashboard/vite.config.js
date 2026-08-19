import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development the page is served by Vite and the API by the controller, so
// /api is proxied across. In production one Go binary serves both and no proxy
// exists, which is why every fetch below uses a relative path.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8082',
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
