import { test, expect, type Page, type Response } from '@playwright/test'

// The test broker (e2e/broker/main.go) always listens on this fixed loopback
// port and bootstraps the admin user with these credentials, so the suite
// exercises the real Broker endpoints end to end.
const BASE_URL = 'http://127.0.0.1:4187'
const ADMIN_USERNAME = 'admin'
const ADMIN_PASSWORD = 'admin1234'

// Accessible-name contract shared with the admin SPA; the suite selects by
// roles and text only, never by CSS structure.
const RESTORE_TEXT = 'Restoring administrator session'
const LOGIN_HEADING = 'Administrator sign in'
const GENERIC_LOGIN_ERROR = 'Invalid username or password'
const UNAVAILABLE_HEADING = 'Broker unavailable'
const ACTIVE_HEADING = 'Administrator session active'

interface ParsedSetCookie {
  name: string
  value: string
  attributes: string[]
}

function parseSetCookie(header: string): ParsedSetCookie {
  const [pair, ...rest] = header.split(';')
  const eq = pair.indexOf('=')
  return {
    name: pair.slice(0, eq).trim(),
    value: pair.slice(eq + 1),
    attributes: rest.map(a => a.trim()).filter(Boolean),
  }
}

// The session cookie must be host-only (no Domain), non-persistent (no
// Max-Age/Expires), HttpOnly, Secure, SameSite=Strict, and scoped to the
// whole origin.
function expectSessionCookieCustody(setCookie: string): void {
  const cookie = parseSetCookie(setCookie)
  expect(cookie.name).toBe('refresh_token')
  expect(cookie.value).not.toBe('')
  const attributes = cookie.attributes.join('; ')
  expect(attributes).toContain('Path=/')
  expect(attributes).toContain('HttpOnly')
  expect(attributes).toContain('Secure')
  expect(attributes).toContain('SameSite=Strict')
  expect(attributes).not.toMatch(/Domain=/i)
  expect(attributes).not.toMatch(/Max-Age=/i)
  expect(attributes).not.toMatch(/Expires=/i)
}

// Auth endpoints never hand the refresh credential to JavaScript: the body
// carries only the access JWT, and the response is uncacheable.
async function expectAuthJSON(response: Response): Promise<void> {
  expect(response.status()).toBe(200)
  expect(response.headers()['cache-control']).toBe('no-store')
  const body = await response.json()
  expect(typeof body.jwt).toBe('string')
  expect(body).not.toHaveProperty('refresh_token')
}

async function setCookieOf(response: Response): Promise<string | undefined> {
  const headers = await response.headersArray()
  return headers.find(h => h.name.toLowerCase() === 'set-cookie')?.value
}

async function signIn(page: Page): Promise<void> {
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
}

test('serves the admin SPA at /admin and /admin/', async ({ page }) => {
  for (const path of ['/admin', '/admin/']) {
    const response = await page.goto(path)
    expect(response?.status()).toBeLessThan(400)
    await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  }
})

test('shows the neutral restore state before the login form on first visit', async ({ page }) => {
  // Hold the restore request open so the neutral state is observable; with
  // no cookie the refresh must resolve to the login form, never to an
  // authenticated view.
  await page.route('**/refresh', async route => {
    await new Promise(resolve => setTimeout(resolve, 500))
    await route.continue()
  })
  await page.goto('/admin/')
  await expect(page.getByText(RESTORE_TEXT)).toBeVisible()
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()
  await page.unroute('**/refresh')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
})

test('rejects bad credentials with a generic error and no session cookie', async ({ page }) => {
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()

  // Wrong password and unknown user must be indistinguishable.
  for (const [username, password] of [
    [ADMIN_USERNAME, 'wrong-password'],
    ['unknown-user', 'wrong-password'],
  ] as const) {
    const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
    await page.getByLabel('Username').fill(username)
    await page.getByLabel('Password').fill(password)
    await page.getByRole('button', { name: 'Sign in' }).click()

    const response = await loginPromise
    expect(response.status()).toBe(401)
    expect(response.headers()['cache-control']).toBe('no-store')
    await expect(page.getByText(GENERIC_LOGIN_ERROR)).toBeVisible()
    await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
    await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).not.toBeVisible()
  }

  const cookies = await page.context().cookies()
  expect(cookies.some(c => c.name === 'refresh_token')).toBe(false)
})

test('signs in and takes custody of a host-only HttpOnly Secure SameSite=Strict session cookie', async ({
  page,
  context,
}) => {
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()

  const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()

  const response = await loginPromise
  await expectAuthJSON(response)
  const setCookie = await setCookieOf(response)
  expect(setCookie).toBeDefined()
  expectSessionCookieCustody(setCookie!)

  // The browser-side cookie matches the same custody contract: host-only,
  // session-scoped (no persistent expiry), HttpOnly, Secure, Strict.
  const cookie = (await context.cookies()).find(c => c.name === 'refresh_token')
  expect(cookie).toBeDefined()
  expect(cookie!.domain).toBe('127.0.0.1')
  expect(cookie!.path).toBe('/')
  expect(cookie!.httpOnly).toBe(true)
  expect(cookie!.secure).toBe(true)
  expect(cookie!.sameSite).toBe('Strict')
  expect(cookie!.expires).toBe(-1)
})

test('reload restores the session silently via cookie rotation', async ({ page, context }) => {
  await signIn(page)
  const tokenBefore = (await context.cookies()).find(c => c.name === 'refresh_token')!.value

  // Hold the restore refresh open so the neutral state is observable and no
  // login form can flash through.
  await page.route('**/refresh', async route => {
    await new Promise(resolve => setTimeout(resolve, 500))
    await route.continue()
  })
  const refreshPromise = page.waitForResponse(r => r.url().endsWith('/refresh') && r.request().method() === 'POST')
  await page.reload()

  await expect(page.getByText(RESTORE_TEXT)).toBeVisible()
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()

  const response = await refreshPromise
  await expectAuthJSON(response)
  const setCookie = await setCookieOf(response)
  expect(setCookie).toBeDefined()
  expectSessionCookieCustody(setCookie!)

  // Rotation replaced the credential: the successor differs from the one the
  // pre-reload session held.
  const tokenAfter = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  expect(tokenAfter).not.toBe(tokenBefore)
})

test('shows Broker unavailable when restore fails and Retry recovers', async ({ page }) => {
  await signIn(page)

  // Aborting client-side means the request never reaches the Broker, so the
  // cookie is not rotated and Retry can still restore with it.
  await page.route('**/refresh', route => route.abort())
  await page.reload()
  await expect(page.getByRole('heading', { name: UNAVAILABLE_HEADING })).toBeVisible()

  await page.unroute('**/refresh')
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
})

test('signs out: revokes before clearing, returns to login, and rejects replay', async ({ page, context }) => {
  await signIn(page)
  const token = (await context.cookies()).find(c => c.name === 'refresh_token')!.value

  const logoutPromise = page.waitForResponse(r => r.url().endsWith('/logout') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()

  const response = await logoutPromise
  expect(response.status()).toBe(204)
  expect(response.headers()['cache-control']).toBe('no-store')

  // The session cookie is gone from the browser profile and the
  // authenticated content is cleared.
  await expect.poll(async () => (await context.cookies()).some(c => c.name === 'refresh_token')).toBe(false)
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).not.toBeVisible()

  // The pre-logout credential was revoked, not just dropped client-side.
  const replay = await page.request.post(`${BASE_URL}/refresh`, {
    headers: { cookie: `refresh_token=${token}` },
  })
  expect(replay.status()).toBe(401)
  expect(replay.headers()['cache-control']).toBe('no-store')
})

test('rejects refresh without a cookie', async ({ page }) => {
  const response = await page.request.post(`${BASE_URL}/refresh`)
  expect(response.status()).toBe(401)
  expect(response.headers()['cache-control']).toBe('no-store')
})
