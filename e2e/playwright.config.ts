import { defineConfig, devices } from '@playwright/test'
import { fileURLToPath } from 'node:url'

// The test broker (e2e/broker/main.go) always listens on this fixed loopback
// port; the spec file hardcodes the same port.
const PORT = 4187
const BASE_URL = `http://127.0.0.1:${PORT}`

// Repository root: web/dist and sql/migrations resolve from here, and the
// broker is launched with this as its working directory.
const repoRoot = fileURLToPath(new URL('..', import.meta.url))

export default defineConfig({
  testDir: '.',
  testMatch: '**/*.spec.ts',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: [
    ['list'],
    [
      'html',
      {
        outputFolder: fileURLToPath(new URL('../tmp/playwright-report', import.meta.url)),
        open: 'never',
      },
    ],
  ],
  outputDir: fileURLToPath(new URL('../tmp/playwright-results', import.meta.url)),
  use: {
    baseURL: BASE_URL,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'go run ./e2e/broker',
    cwd: repoRoot,
    url: `${BASE_URL}/admin/`,
    timeout: 180_000,
    reuseExistingServer: !process.env.CI,
    stdout: 'pipe',
    stderr: 'pipe',
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
})
