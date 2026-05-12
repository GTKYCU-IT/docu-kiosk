import path from 'path'
import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
  },
  server: {
    proxy: {
      '/api': { target: 'https://localhost:8080', secure: false },
      '/ws': { target: 'wss://localhost:8080', ws: true, secure: false },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
