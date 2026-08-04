import path from 'node:path'
import { crx } from '@crxjs/vite-plugin'
import tailwindcss from '@tailwindcss/vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vitest/config'
import zip from 'vite-plugin-zip-pack'
import manifest from './manifest.config.js'
import { name, version } from './package.json'

export default defineConfig({
  resolve: {
    alias: {
      '@': `${path.resolve(__dirname, 'src')}`,
      '$lib': path.resolve(__dirname, 'src/lib'),
    },
  },
  plugins: [
    tailwindcss(),
    svelte(),
    crx({ manifest }),
    zip({ outDir: 'release', outFileName: `crx-${name}-${version}.zip` }),
  ],

  // CRXJS only builds HTML pages referenced in manifest fields (options_page,
  // default_popup, ...). The interception redirect target isn't one of those,
  // so register it explicitly as a Vite multi-page entry.
  build: {
    rollupOptions: {
      input: {
        intercepted: path.resolve(__dirname, 'src/intercepted/index.html'),
      },
    },
  },
  server: {
    cors: {
      origin: [
        /chrome-extension:\/\//,
      ],
    },
  },
  test: {
    passWithNoTests: true,
  },
})
