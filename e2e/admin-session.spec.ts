import { test, expect, type BrowserContext, type Page, type Response } from '@playwright/test'

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

// #55: cross-tab session coordination

async function openAuthenticatedPeerTab(context: BrowserContext): Promise<Page> {
  const peer = await context.newPage()
  await peer.goto('/admin/')
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  return peer
}

function trackAuthPosts(context: BrowserContext, path: string): () => number {
  let count = 0
  context.on('request', request => {
    if (request.url().endsWith(path) && request.method() === 'POST') count += 1
  })
  return () => count
}

test('a new tab restores from the peer access JWT without refreshing', async ({ page, context }) => {
  await signIn(page)
  const refreshCount = trackAuthPosts(context, '/refresh')
  const tokenBefore = (await context.cookies()).find(c => c.name === 'refresh_token')!.value

  const peer = await openAuthenticatedPeerTab(context)

  expect(refreshCount()).toBe(0)
  const tokenAfter = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  expect(tokenAfter).toBe(tokenBefore)
})

test('concurrent near-expiry restores rotate the cookie exactly once and converge', async ({
  page,
  context,
}) => {
  // The shared access JWT is aged into the safety window on the real clock,
  // so the concurrent restore race needs the full fifteen-second life budget.
  test.setTimeout(60_000)

  // Sign in manually so the exact access JWT the two tabs share is
  // observable and its real exp can drive the wait into the window.
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  const loginResponse = await loginPromise
  const accessJwt = (await loginResponse.json()).jwt

  const peer = await openAuthenticatedPeerTab(context)

  const refreshCount = trackAuthPosts(context, '/refresh')
  const tokenBefore = (await context.cookies()).find(c => c.name === 'refresh_token')!.value

  // Age the shared JWT inside the five-second expiry safety window in real
  // time: neither tab may restore from its peer, so both reloads demand
  // /refresh and the cross-tab lock must serialize the exchange.
  await waitUntilAccessJwtInsideSafetyWindow(page, accessJwt)

  await Promise.all([page.reload(), peer.reload()])
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()
  await expect(peer.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()

  expect(refreshCount()).toBe(1)
  const tokenAfter = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  expect(tokenAfter).not.toBe(tokenBefore)
})

// The broker's two reusable credentials have recognizable shapes: the access
// token is an HS256 JWT (three dot-separated base64url segments) and the
// refresh token is hex(randomblob(32)), sixty-four lowercase hex digits.
// Anything JS-readable that equals a live credential, embeds one, or matches
// either shape is a leak, so the storage test needs no blanket emptiness.
const ACCESS_JWT_SHAPE = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/
const REFRESH_TOKEN_SHAPE = /^[0-9a-f]{64}$/

async function inventoryJsReadableStorage(tab: Page): Promise<Record<string, string[]>> {
  return tab.evaluate(async () => {
    const inventory: Record<string, string[]> = {}
    const push = (surface: string, entry: string) => {
      (inventory[surface] ??= []).push(entry)
    }
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i) ?? ''
      push('localStorage', key)
      push('localStorage', localStorage.getItem(key) ?? '')
    }
    for (let i = 0; i < sessionStorage.length; i++) {
      const key = sessionStorage.key(i) ?? ''
      push('sessionStorage', key)
      push('sessionStorage', sessionStorage.getItem(key) ?? '')
    }
    for (const database of await indexedDB.databases()) {
      push('indexedDB', database.name ?? '')
    }
    for (const name of await caches.keys()) {
      push('cacheStorage', name)
    }
    for (const pair of document.cookie.split(';')) {
      const trimmed = pair.trim()
      const eq = trimmed.indexOf('=')
      push('cookies', eq === -1 ? trimmed : trimmed.slice(0, eq))
      push('cookies', eq === -1 ? '' : trimmed.slice(eq + 1))
    }
    return inventory
  })
}

test('keeps credentials out of JavaScript-visible and durable storage', async ({ page, context }) => {
  // Sign in manually so the exact access JWT the login issued is observable.
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  const accessJwt = (await (await loginPromise).json()).jwt

  // The refresh credential lives for the browser profile alone.
  const refreshToken = (await context.cookies()).find(c => c.name === 'refresh_token')!.value

  const peer = await openAuthenticatedPeerTab(context)

  // HttpOnly custody: the refresh cookie must never surface in document.cookie.
  for (const [label, tab] of [['first tab', page], ['peer tab', peer]] as const) {
    const inventory = await inventoryJsReadableStorage(tab)
    for (const [surface, entries] of Object.entries(inventory)) {
      for (const entry of entries) {
        expect(entry, `${label} ${surface} entry must not contain the access JWT`).not.toContain(accessJwt)
        expect(entry, `${label} ${surface} entry must not contain the refresh token`).not.toContain(refreshToken)
        expect(
          ACCESS_JWT_SHAPE.test(entry) || REFRESH_TOKEN_SHAPE.test(entry),
          `${label} ${surface} entry must not be token-shaped`,
        ).toBe(false)
      }
    }
  }

  // The session credential still lives for the browser profile alone.
  const cookie = (await context.cookies()).find(c => c.name === 'refresh_token')
  expect(cookie).toBeDefined()
  expect(cookie!.httpOnly).toBe(true)
})

// #55: terminal auth loss and sign-out convergence across tabs

const LOGOUT_FAILED_TEXT = 'Sign out failed. Your administrator session is still active.'

async function expectSignInScreen(page: Page): Promise<void> {
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).not.toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign out' })).not.toBeVisible()
}

// Waits on the page's real clock until the access JWT has certainly
// expired: idle tabs schedule no refresh, so the terminal 401 can only be
// provoked by a restore or protected demand, never by the clock alone.
async function waitUntilAccessJwtExpired(page: Page, jwt: string): Promise<void> {
  const delay = accessJwtExpiryMs(jwt) - Date.now() + 1000
  if (delay > 0) {
    await page.waitForTimeout(delay)
  }
}

test('terminal authentication loss after access-JWT expiry converges every same-profile tab to sign in', async ({
  page,
  context,
}) => {
  // The access JWT lives only fifteen seconds and is aged on the real
  // clock, so the demand-driven expiry cycle needs room to finish.
  test.setTimeout(60_000)

  // Sign in manually so the exact access JWT the two tabs share is
  // observable and its real exp can drive the expiry wait.
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  const loginResponse = await loginPromise
  const accessJwt = (await loginResponse.json()).jwt

  const peer = await openAuthenticatedPeerTab(context)

  // Revoke the refresh credential server-side, as a login from elsewhere
  // would. Both tabs still hold valid in-memory access JWTs, so only a
  // demand-driven refresh refusal can end the session.
  const token = (await context.cookies()).find(c => c.name === 'refresh_token')
  expect(token).toBeDefined()
  const revoke = await page.request.post(`${BASE_URL}/logout`, {
    headers: { cookie: `refresh_token=${token!.value}` },
  })
  expect(revoke.status()).toBe(204)

  const refreshCount = trackAuthPosts(context, '/refresh')

  // Wait out the real access-JWT life with both tabs untouched: idle tabs
  // schedule no refresh, so the session still looks alive and the refresh
  // endpoint stays silent until a demand arrives.
  await waitUntilAccessJwtExpired(page, accessJwt)
  expect(refreshCount()).toBe(0)
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()

  // The reload's /refresh refusal is the single exchange that broadcasts the
  // terminal state to the still-open peer tab.
  const refresh401 = page.waitForResponse(r => r.url().endsWith('/refresh') && r.request().method() === 'POST')
  await page.reload()
  const response = await refresh401
  expect(response.status()).toBe(401)
  expect(response.headers()['cache-control']).toBe('no-store')

  await expectSignInScreen(page)
  await expectSignInScreen(peer)
  expect(refreshCount()).toBe(1)
})

test('signing out in one tab removes the authenticated UI from every same-profile tab', async ({
  page,
  context,
}) => {
  await signIn(page)
  const peer = await openAuthenticatedPeerTab(context)

  const refreshCount = trackAuthPosts(context, '/refresh')
  const loginCount = trackAuthPosts(context, '/login')
  const logoutCount = trackAuthPosts(context, '/logout')

  const logoutPromise = page.waitForResponse(r => r.url().endsWith('/logout') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Sign out' }).click()
  const response = await logoutPromise
  expect(response.status()).toBe(204)

  await expectSignInScreen(page)
  await expectSignInScreen(peer)
  expect(refreshCount()).toBe(0)
  expect(loginCount()).toBe(0)
  expect(logoutCount()).toBe(1)
})

test('failed sign out keeps every same-profile tab authenticated and explains the failure', async ({
  page,
  context,
}) => {
  await signIn(page)
  const peer = await openAuthenticatedPeerTab(context)
  const refreshCount = trackAuthPosts(context, '/refresh')
  const loginCount = trackAuthPosts(context, '/login')
  const logoutCount = trackAuthPosts(context, '/logout')

  await page.route('**/logout', route =>
    route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"logout failed"}' }),
  )
  await page.getByRole('button', { name: 'Sign out' }).click()

  await expect(page.getByText(LOGOUT_FAILED_TEXT, { exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(peer.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()

  expect(refreshCount()).toBe(0)
  expect(loginCount()).toBe(0)
  expect(logoutCount()).toBe(1)
  expect((await context.cookies()).some(c => c.name === 'refresh_token')).toBe(true)

  await page.unroute('**/logout')
  const logoutPromise = page.waitForResponse(r => r.url().endsWith('/logout') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Sign out' }).click()
  const response = await logoutPromise
  expect(response.status()).toBe(204)

  await expectSignInScreen(page)
  await expectSignInScreen(peer)
  expect(logoutCount()).toBe(2)
})

// #55: near-expiry and reload restoration alongside a living peer

// Decodes the exp claim of the access JWT the broker issues (standard
// RegisteredClaims, fifteen-second life) so a test can wait on the real
// lifetime instead of a magic constant.
function accessJwtExpiryMs(jwt: string): number {
  const payload = jwt.split('.')[1]!
  const base64 = payload.replace(/-/g, '+').replace(/_/g, '/')
  return JSON.parse(Buffer.from(base64, 'base64').toString('utf8')).exp * 1000
}

// Waits on the page's real clock until the access JWT is inside the
// five-second expiry safety window but not yet expired: a peer must refuse
// to share it, so a restore demand deterministically falls through to
// /refresh without waiting out the full fifteen-second life.
async function waitUntilAccessJwtInsideSafetyWindow(page: Page, jwt: string): Promise<void> {
  // Target three seconds of remaining life: comfortably inside the window
  // and comfortably short of real expiry for timing skew.
  const remaining = accessJwtExpiryMs(jwt) - Date.now()
  const delay = Math.max(0, remaining - 3000)
  if (delay > 0) {
    await page.waitForTimeout(delay)
  }
}

test('restoring a third tab while a living peer holds a near-expiry access JWT issues one refresh that every tab converges on', async ({
  page,
  context,
}) => {
  // Sign in manually so the exact access JWT the tab holds is observable.
  await page.goto('/admin/')
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).toBeVisible()
  const loginPromise = page.waitForResponse(r => r.url().endsWith('/login') && r.request().method() === 'POST')
  await page.getByLabel('Username').fill(ADMIN_USERNAME)
  await page.getByLabel('Password').fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  const loginResponse = await loginPromise
  const accessJwt = (await loginResponse.json()).jwt

  const peer = await openAuthenticatedPeerTab(context)

  // Age the shared JWT inside the safety window in real time. The peer is
  // still alive but must refuse to share its JWT, so the third tab's demand
  // deterministically rotates the credential exactly once.
  await waitUntilAccessJwtInsideSafetyWindow(page, accessJwt)

  const refreshCount = trackAuthPosts(context, '/refresh')
  const tokenBefore = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  await openAuthenticatedPeerTab(context)

  expect(refreshCount()).toBe(1)
  const tokenAfter = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  expect(tokenAfter).not.toBe(tokenBefore)
  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
})

test('a reload in a two-tab session restores from the live peer access JWT without refreshing', async ({
  page,
  context,
}) => {
  await signIn(page)
  const peer = await openAuthenticatedPeerTab(context)

  const refreshCount = trackAuthPosts(context, '/refresh')
  const tokenBefore = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  await page.reload()

  await expect(page.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  await expect(page.getByRole('heading', { name: LOGIN_HEADING })).not.toBeVisible()
  await expect(peer.getByRole('heading', { name: ACTIVE_HEADING })).toBeVisible()
  expect(refreshCount()).toBe(0)
  const tokenAfter = (await context.cookies()).find(c => c.name === 'refresh_token')!.value
  expect(tokenAfter).toBe(tokenBefore)
})
