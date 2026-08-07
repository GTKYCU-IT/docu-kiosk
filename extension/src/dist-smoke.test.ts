import { describe, it, expect } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// Regression guard for the "blank screen on interception" bug: CRXJS only builds
// HTML pages referenced in manifest fields, so the interception target had to be
// registered as an explicit Vite MPA entry. If that wiring ever regresses, this
// page is emitted as raw source (still referencing ./main.ts) and loads nothing.
// CI runs `npm run build` before `npm test`, so dist/ is always fresh there.
const distDir = fileURLToPath(new URL('../dist', import.meta.url))
const hasDist = existsSync(distDir)

describe.skipIf(!hasDist)('packaged extension (dist/)', () => {
  it('builds the intercepted page as a real page, not raw source', () => {
    const html = readFileSync(join(distDir, 'src/intercepted/index.html'), 'utf8')
    expect(html).toContain('<title>Send to Kiosk</title>')
    expect(html).toMatch(/<script type="module"[^>]*src="\/assets\//)
    expect(html).not.toContain('./main.ts')
  })

  it('builds the options page separately with its own bundle', () => {
    const html = readFileSync(join(distDir, 'src/options/index.html'), 'utf8')
    expect(html).toContain('<title>DocuKiosk Settings</title>')
    expect(html).toMatch(/<script type="module"[^>]*src="\/assets\//)
    expect(html).not.toContain('./main.ts')
  })

  it('emits a background service worker that installs the interception rules', () => {
    const bgFile = readdirSync(join(distDir, 'assets'))
      .find((f) => f.startsWith('background.ts-') && f.endsWith('.js'))
    expect(bgFile).toBeTruthy()
    const code = readFileSync(join(distDir, 'assets', bgFile!), 'utf8')
    expect(code).toContain('updateDynamicRules')
    expect(code).toContain('docusign.net')
    expect(code).toContain('docusign.com')
  })

  it('wires the manifest: separate options page, no popup, intercepted page web-accessible', () => {
    const manifest = JSON.parse(readFileSync(join(distDir, 'manifest.json'), 'utf8')) as {
      options_page?: string
      action?: { default_popup?: unknown }
      web_accessible_resources?: { resources: string[] }[]
    }
    expect(manifest.options_page).toBe('src/options/index.html')
    expect(manifest.action?.default_popup).toBeUndefined()
    const war = manifest.web_accessible_resources?.flatMap((e) => e.resources) ?? []
    expect(war).toContain('src/intercepted/index.html')
  })
})
