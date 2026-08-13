import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'
import { BYPASS_RULE_START_ID, SIGNING_URL_FILTERS, createChromeBypass } from './bypass'
import { createFakeBypass, type FakeBypass } from './bypass.fake'

// The module is imported once, statically, and its top level only defines
// constants — importing it never touches chrome (this file loads with chrome
// undefined and would fail loudly on any import-time chrome access).

// ---------------------------------------------------------------------------
// Chrome-backed adapter: minimal chrome stub for the declarative-net-request,
// storage.session, tabs, and runtime APIs the adapter touches. storage.session
// is backed by a stateful Map, so values survive adapter recreation
// ("service-worker restart" = create a new adapter over the same map;
// "browser restart" = clear the map).
// ---------------------------------------------------------------------------
const sessionStore = new Map<string, unknown>()
type DnrUpdate = { removeRuleIds: number[]; addRules?: chrome.declarativeNetRequest.Rule[] }
const updateDynamicRules = vi.fn<(update: DnrUpdate) => Promise<void>>()
const updateSessionRules = vi.fn<(update: DnrUpdate) => Promise<void>>()
const storageSessionGet = vi.fn(async (key: string) => {
  const value = sessionStore.get(key)
  return value === undefined ? {} : { [key]: value }
})
const storageSessionSet = vi.fn(async (items: Record<string, unknown>) => {
  for (const [key, value] of Object.entries(items)) sessionStore.set(key, value)
})
const tabsCreate = vi.fn()
const tabsUpdate = vi.fn()
const tabsRemove = vi.fn()
const runtimeGetURL = vi.fn((p: string) => `chrome-extension://testid/${p}`)

vi.stubGlobal('chrome', {
  declarativeNetRequest: { updateDynamicRules, updateSessionRules },
  storage: { session: { get: storageSessionGet, set: storageSessionSet } },
  tabs: { create: tabsCreate, update: tabsUpdate, remove: tabsRemove },
  runtime: { getURL: runtimeGetURL },
})

beforeEach(() => {
  sessionStore.clear()
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  updateSessionRules.mockResolvedValue(undefined)
  tabsCreate.mockResolvedValue({ id: 99 })
  tabsUpdate.mockResolvedValue(undefined)
  tabsRemove.mockResolvedValue(undefined)
})

afterAll(() => vi.unstubAllGlobals())

// ---------------------------------------------------------------------------
// Rule/filter behavior — the signing-entry-point patterns themselves.
// ---------------------------------------------------------------------------

// A URL matches the interception rules if any of the short per-pattern rules match.
const matchAnySigningFilter = (url: string) =>
  SIGNING_URL_FILTERS.some((f) => new RegExp(f).test(url))

describe('SIGNING_URL_FILTERS', () => {
  it('matches legacy docusign.net signing URLs (classic embedded signing)', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/Signing/StartInSession.aspx?t=abc123')).toBe(true)
    expect(matchAnySigningFilter('https://na2.docusign.net/Signing/SessionInitiate.aspx?t=abc123')).toBe(true)
  })

  it('matches current email signing links (MTRedeem) on www.docusign.net', () => {
    expect(matchAnySigningFilter('https://www.docusign.net/Signing/MTRedeem/v1/5a948afa-34a9-441b-8919-4033ee57d46c/na?slt=eyJ0eXAiOiJNVCJ9.long-signed-token')).toBe(true)
  })

  it('matches signing URLs with very long tokens (up to 4000 chars per DocuSign)', () => {
    const longToken = 'x'.repeat(3000)
    expect(matchAnySigningFilter(`https://www.docusign.net/Signing/MTRedeem/v1/abc/na?slt=${longToken}`)).toBe(true)
  })

  it('matches StartInSession signing links with a code= parameter', () => {
    expect(matchAnySigningFilter('https://www.docusign.net/Signing/StartInSession.aspx?code=eyJ0...&persistent_auth_token=no_client_token')).toBe(true)
  })

  it('matches lowercase /signing/ paths', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/signing/session/abc123')).toBe(true)
  })

  it('matches PowerForm signing URLs', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/Member/PowerFormSigning.aspx?PowerFormId=abc-123&env=na2')).toBe(true)
  })

  it('matches the new apps.docusign.com/authenticate signing host', () => {
    expect(matchAnySigningFilter('https://apps.docusign.com/authenticate?token=eyJ0eXAiOiJNVCJ9.very-long-token-up-to-4000-chars')).toBe(true)
    expect(matchAnySigningFilter('https://apps.docusign.com/authenticate/abc123')).toBe(true)
  })

  it('does NOT match the staff-facing DocuSign web app', () => {
    for (const url of [
      'https://app.docusign.com/home',
      'https://app.docusign.com/documents?view=sent',
      'https://account.docusign.com/login',
      'https://www.docusign.com/',
      'https://support.docusign.com/s/article/help',
      'https://docusign.com/',
    ]) {
      expect(matchAnySigningFilter(url)).toBe(false)
    }
  })

  it('does NOT match non-signing paths on signing hosts', () => {
    for (const url of [
      'https://apps.docusign.com/',
      'https://apps.docusign.com/documents',
      'https://demo.docusign.net/Member/MemberLogin.aspx',
      'https://demo.docusign.net/webFile.aspx?docId=123',
      'https://demo.docusign.net/AuthorizeWithMFA.aspx',
    ]) {
      expect(matchAnySigningFilter(url)).toBe(false)
    }
  })

  it('does NOT match non-DocuSign hosts', () => {
    expect(matchAnySigningFilter('https://broker.internal/api/kiosks')).toBe(false)
    expect(matchAnySigningFilter('https://example.com/Signing/StartInSession.aspx')).toBe(false)
  })
})

describe('RE2 compatibility (Chrome DNR regexFilter syntax)', () => {
  // Chrome's declarativeNetRequest regexFilter follows RE2 syntax, which
  // lacks lookarounds, atomic groups, and backreferences. Keep it that way.
  const forbidden = ['(?=', '(?!', '(?<=', '(?<!', '(?>', '\\1', '\\2', '\\b']

  it('avoids RE2-unsupported constructs', () => {
    for (const pattern of SIGNING_URL_FILTERS) {
      for (const token of forbidden) {
        expect(pattern, `pattern should not contain ${token}`).not.toContain(token)
      }
    }
  })

  it('keeps every regex well under Chrome\'s 2KB compiled-regex rule limit', () => {
    // Chrome rejects regexFilters whose compiled program exceeds ~2KB (a single
    // multi-alternation regex was ~5.7KB and silently never installed). RE2
    // program size tracks pattern length; 80 chars keeps each pattern near the
    // ~35-43 instruction size of the filter that shipped in production.
    for (const f of SIGNING_URL_FILTERS) {
      expect(f.length).toBeLessThanOrEqual(80)
    }
  })
})

// ---------------------------------------------------------------------------
// The real lifecycle through the in-memory harness.
// ---------------------------------------------------------------------------

let harness: FakeBypass

beforeEach(() => {
  harness = createFakeBypass()
})

const testUrl = 'https://demo.docusign.net/Signing/StartInSession.aspx?t=abc'

/** IDs of the session rules currently scoped to `tabId` on the harness. */
function rulesForTab(tabId: number): number[] {
  return [...harness.sessionRules.values()]
    .filter((r) => r.condition.tabIds?.includes(tabId))
    .map((r) => r.id)
}

/** An old intercept rule a prior worker run might have left installed. */
function makeStaleRule(id: number): chrome.declarativeNetRequest.Rule {
  return {
    id,
    priority: 1,
    action: {
      type: 'redirect',
      redirect: { regexSubstitution: 'https://stale.invalid/#url=\\0' },
    },
    condition: { regexFilter: '^stale$', resourceTypes: ['main_frame'] },
  }
}

describe('createFakeBypass — intercept rule installation', () => {
  it('installs the four intercept rules onto a pristine harness at startup', async () => {
    await harness.installIntercept()
    expect(harness.dynamicRules.size).toBe(4)
    const rules = [...harness.dynamicRules.values()]
    expect(rules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(rules.map((r) => r.condition.regexFilter)).toEqual(SIGNING_URL_FILTERS)
  })

  it('creates one short regex rule per signing pattern, redirecting via #url=\\0', async () => {
    await harness.installIntercept()
    const rules = [...harness.dynamicRules.values()]
    expect(rules).toHaveLength(4)
    expect(rules.every((r) => r.condition.initiatorDomains === undefined)).toBe(true)
    expect(rules.every((r) => r.action.type === 'redirect')).toBe(true)
    expect(rules.every((r) => String(r.action.redirect?.regexSubstitution).includes('#url=\\0'))).toBe(true)
  })

  it('replaces stale intercept rules from a prior worker run at startup', async () => {
    // Dynamic-scope rules survive a service-worker restart, so a prior run's
    // intercept rules (IDs 1-4) can still be installed when this worker boots.
    // The startup install must sweep them before adding the fresh set.
    for (let id = 1; id <= 4; id++) harness.dynamicRules.set(id, makeStaleRule(id))
    await harness.installIntercept()

    expect(harness.dynamicRules.size).toBe(4)
    const rules = [...harness.dynamicRules.values()]
    expect(rules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(rules.map((r) => r.condition.regexFilter)).toEqual(SIGNING_URL_FILTERS)
    expect(rules.some((r) => r.condition.regexFilter === '^stale$')).toBe(false)
  })

  it('logs a visible error when the DNR update rejects the rules', async () => {
    harness.dnr.updateDynamicRules.mockRejectedValueOnce(
      new Error('Rule with id 1 was skipped')
    )
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    await harness.installIntercept()
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] failed to install interception rules:',
      expect.any(Error)
    )
  })
})

describe('createFakeBypass — open', () => {
  it('creates a blank tab, installs session allow rules, navigates, and returns the tab id', async () => {
    const tabId = await harness.open(testUrl)

    expect(tabId).toBe(99)
    expect(harness.tabs.create).toHaveBeenCalledWith({ url: 'about:blank', active: false })
    expect(harness.sessionRules.size).toBe(2)
    const rules = [...harness.sessionRules.values()]
    expect(rules.map((r) => r.id)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rules.every((r) => r.action.type === 'allow')).toBe(true)
    expect(rules.map((r) => r.condition.tabIds)).toEqual([[99], [99]])
    expect(harness.tabs.update).toHaveBeenCalledWith(99, { url: testUrl, active: true })
  })

  it('creates two allow rules for a bypass tab, one per DocuSign host', async () => {
    await harness.open(testUrl)
    const rules = [...harness.sessionRules.values()]
    expect(rules).toHaveLength(2)
    expect(rules.every((r) => r.priority === 100)).toBe(true)
    expect(rules.every((r) => r.condition.resourceTypes?.includes('main_frame' as chrome.declarativeNetRequest.ResourceType))).toBe(true)
    expect(rules.map((r) => r.condition.urlFilter)).toEqual([
      '*://*.docusign.net/*',
      '*://*.docusign.com/*',
    ])
  })

  it('produces allow rules that out-prioritise the intercept redirect rules', async () => {
    await harness.installIntercept()
    await harness.open(testUrl)
    const bypassPriorities = [...harness.sessionRules.values()].map((r) => r.priority!)
    const interceptPriorities = [...harness.dynamicRules.values()].map((r) => r.priority!)
    for (const bp of bypassPriorities) {
      for (const ip of interceptPriorities) {
        expect(bp).toBeGreaterThan(ip)
      }
    }
  })

  it('creates the blank tab before installing rules and navigates only after', async () => {
    await harness.open(testUrl)
    expect(harness.tabs.create).toHaveBeenCalledBefore(harness.dnr.updateSessionRules)
    expect(harness.dnr.updateSessionRules).toHaveBeenCalledBefore(harness.tabs.update)
  })

  it('sweeps the allocated IDs and adds the new rules in one atomic update', async () => {
    await harness.open(testUrl)
    expect(harness.dnr.updateSessionRules).toHaveBeenCalledTimes(1)
    const update = harness.dnr.updateSessionRules.mock.calls[0][0]
    expect(update.removeRuleIds).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(update.addRules).toHaveLength(2)
    expect(update.addRules!.map((r) => r.id)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })

  it('persists the tab→rule mapping for the bypass tab', async () => {
    await harness.open(testUrl)
    expect(harness.tabRuleIds.get(99)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })

  it('allocates consecutive session-persisted IDs from 100', async () => {
    await harness.open(testUrl)
    await harness.open(testUrl)
    expect([...harness.sessionRules.keys()]).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
  })
})

describe('createFakeBypass — multi-tab bypass', () => {
  it('scopes each rule to its own tab and removes only the closed tab rules', async () => {
    harness.tabs.create
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await harness.open('https://demo.docusign.net/Signing/a')
    await harness.open('https://demo.docusign.net/Signing/b')

    expect(harness.sessionRules.size).toBe(4)
    expect([...harness.sessionRules.keys()]).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
    expect(rulesForTab(10)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rulesForTab(20)).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])

    await harness.close(10)
    expect([...harness.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])
    expect([...harness.tabRuleIds.keys()]).toEqual([20])
  })
})

describe('createFakeBypass — tab close cleanup', () => {
  it("removes the tab's session rules and mapping when the tab closes", async () => {
    await harness.open(testUrl)
    await harness.close(99)
    expect(harness.sessionRules.size).toBe(0)
    expect(harness.tabRuleIds.size).toBe(0)
  })

  it('is a no-op for unknown tab ids', async () => {
    await harness.close(999)
    expect(harness.sessionRules.size).toBe(0)
    expect(harness.dnr.updateSessionRules).not.toHaveBeenCalled()
  })

  it("removes a closed tab's rules after a service-worker restart", async () => {
    harness.tabs.create.mockResolvedValueOnce({ id: 30 })
    await harness.open('https://demo.docusign.net/Signing/a')
    expect(harness.sessionRules.size).toBe(2)
    expect(harness.tabRuleIds.get(30)).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
    ])

    // Service-worker restart: a fresh module instance over the same persisted
    // state; its tab-removed listener (here: close) still finds the mapping.
    const restarted = harness.newBypass()
    await restarted.close(30)
    expect(harness.sessionRules.size).toBe(0)
    expect(harness.tabRuleIds.size).toBe(0)
  })

  it("keeps other tabs' rules when one tab closes after a service-worker restart", async () => {
    harness.tabs.create
      .mockResolvedValueOnce({ id: 30 })
      .mockResolvedValueOnce({ id: 31 })
    await harness.open('https://demo.docusign.net/Signing/a')
    await harness.open('https://demo.docusign.net/Signing/b')

    const restarted = harness.newBypass()
    await restarted.close(30)
    expect(harness.sessionRules.size).toBe(2)
    expect(rulesForTab(31)).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])
    expect([...harness.tabRuleIds.keys()]).toEqual([31])
  })

  it('starts with an empty tab→rule map after a browser restart', async () => {
    await harness.open(testUrl)
    expect(harness.tabRuleIds.size).toBe(1)

    // Browser restart: fresh harness (session rules, counter, and tab map
    // cleared), as the fresh worker's entry would see it.
    const fresh = createFakeBypass()
    expect(fresh.tabRuleIds.size).toBe(0)
    await fresh.close(99)
    expect(fresh.sessionRules.size).toBe(0)
  })
})

describe('createFakeBypass — service-worker and browser restarts', () => {
  it('continues rule IDs across service-worker restarts', async () => {
    harness.tabs.create.mockResolvedValueOnce({ id: 30 })
    await harness.open('https://demo.docusign.net/Signing/a')
    expect([...harness.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])

    // Service-worker restart: a fresh module instance against the SAME
    // harness primitives — its session rules and ID counter outlive the worker.
    const restarted = harness.newBypass()
    harness.tabs.create.mockResolvedValueOnce({ id: 31 })
    await restarted.open('https://demo.docusign.net/Signing/b')

    expect(harness.sessionRules.size).toBe(4)
    expect([...harness.sessionRules.keys()]).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
  })

  it('restarts rule IDs at BYPASS_RULE_START_ID after a browser restart', async () => {
    await harness.open(testUrl)
    expect([...harness.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])

    // Browser restart: fresh harness (session rules and counter cleared).
    const fresh = createFakeBypass()
    fresh.tabs.create.mockResolvedValueOnce({ id: 40 })
    await fresh.open('https://demo.docusign.net/Signing/b')
    expect([...fresh.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })

  it('sweeps stale colliding rules instead of duplicating them', async () => {
    await harness.open(testUrl)
    expect(harness.sessionRules.size).toBe(2)

    // Counter drifts back to BYPASS_RULE_START_ID (e.g. the session counter
    // was cleared) while the session rules from the earlier bypass are still
    // installed.
    harness.driftCounter()
    harness.tabs.create.mockResolvedValueOnce({ id: 60 })
    await harness.open('https://demo.docusign.net/Signing/c')

    // Sweep-then-add replaced the stale rules — no "Rule with id N does not
    // have a unique ID" rejection, and no duplicates.
    expect(harness.sessionRules.size).toBe(2)
    expect([...harness.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rulesForTab(60)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })
})

describe('createFakeBypass — failure cleanup', () => {
  it('removes the orphan blank tab when the bypass install fails', async () => {
    harness.dnr.updateSessionRules.mockRejectedValueOnce(new Error('boom'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    await expect(harness.open(testUrl)).rejects.toThrow('boom')
    expect(harness.tabs.remove).toHaveBeenCalledWith(99)
    expect(consoleError).toHaveBeenCalledWith('[docu-kiosk] bypass failed:', expect.any(Error))
  })

  it('still navigates and logs when persisting the tab mapping fails', async () => {
    // The first storage write is the ID-counter advance and must succeed; the
    // second is the tab→rule mapping and is the one that fails.
    harness.storage.set.mockResolvedValueOnce(undefined)
    harness.storage.set.mockRejectedValueOnce(new Error('storage full'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    await harness.open(testUrl)

    // The rules are installed; a persistence failure must not fail the bypass.
    expect(harness.tabs.update).toHaveBeenCalledWith(99, { url: testUrl, active: true })
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] bypass state persist failed:',
      expect.any(Error)
    )
  })

  it('logs a failed tab-close cleanup and keeps the mapping so a retry can clean up', async () => {
    await harness.open(testUrl)
    harness.dnr.updateSessionRules.mockRejectedValueOnce(new Error('storage full'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    await harness.close(99)
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] bypass cleanup failed:',
      expect.any(Error)
    )
    // Nothing was torn down on failure — rules and mapping stay consistent.
    expect(harness.sessionRules.size).toBe(2)
    expect(harness.tabRuleIds.size).toBe(1)

    // The mapping was retained, so a later close retries the removal.
    await harness.close(99)
    expect(harness.sessionRules.size).toBe(0)
    expect(harness.tabRuleIds.size).toBe(0)
  })
})

describe('createFakeBypass — concurrency', () => {
  it('allocates distinct rule IDs under concurrent opens', async () => {
    harness.tabs.create
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await Promise.all([
      harness.open('https://demo.docusign.net/Signing/a'),
      harness.open('https://demo.docusign.net/Signing/b'),
    ])

    expect([...harness.sessionRules.keys()].sort((a, b) => a - b)).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
    expect(rulesForTab(10)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rulesForTab(20)).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])
  })

  it('concurrent closes do not resurrect entries', async () => {
    harness.tabs.create
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })
    await harness.open('https://demo.docusign.net/Signing/a')
    await harness.open('https://demo.docusign.net/Signing/b')

    await Promise.all([harness.close(10), harness.close(20)])

    expect(harness.tabRuleIds.size).toBe(0)
    expect(harness.sessionRules.size).toBe(0)
  })
})

// ---------------------------------------------------------------------------
// Chrome-backed adapter plumbing.
// ---------------------------------------------------------------------------

describe('createChromeBypass', () => {
  it('defers all chrome access until a method is called', () => {
    createChromeBypass()
    expect(updateDynamicRules).not.toHaveBeenCalled()
    expect(updateSessionRules).not.toHaveBeenCalled()
    expect(storageSessionGet).not.toHaveBeenCalled()
    expect(storageSessionSet).not.toHaveBeenCalled()
    expect(tabsCreate).not.toHaveBeenCalled()
    expect(runtimeGetURL).not.toHaveBeenCalled()
  })

  it('installs interception rules via updateDynamicRules', async () => {
    await createChromeBypass().installIntercept()
    expect(runtimeGetURL).toHaveBeenCalledWith('src/intercepted/index.html')
    expect(updateDynamicRules).toHaveBeenCalledWith({
      removeRuleIds: [1, 2, 3, 4],
      addRules: expect.any(Array),
    })
    const update = updateDynamicRules.mock.calls[0][0]
    expect(update.addRules).toHaveLength(4)
    expect(update.addRules!.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(update.addRules!.map((r) => r.condition.regexFilter)).toEqual(SIGNING_URL_FILTERS)
  })

  it('opens a blank tab, installs allow rules, and navigates', async () => {
    const bypass = createChromeBypass()
    const tabId = await bypass.open(testUrl)

    expect(tabId).toBe(99)
    expect(tabsCreate).toHaveBeenCalledWith({ url: 'about:blank', active: false })
    expect(updateSessionRules).toHaveBeenCalledTimes(1)
    const update = updateSessionRules.mock.calls[0][0]
    expect(update.removeRuleIds).toEqual([100, 101])
    expect(update.addRules).toHaveLength(2)
    expect(update.addRules!.map((r) => r.id)).toEqual([100, 101])
    expect(update.addRules!.every((r) => r.action.type === 'allow')).toBe(true)
    expect(update.addRules!.map((r) => r.condition.tabIds)).toEqual([[99], [99]])
    expect(storageSessionSet).toHaveBeenCalledWith({ bypassNextRuleId: 102 })
    expect(tabsUpdate).toHaveBeenCalledWith(99, { url: testUrl, active: true })
  })

  it('allocates consecutive IDs from the persisted counter', async () => {
    const bypass = createChromeBypass()
    await bypass.open(testUrl)
    await bypass.open(testUrl)
    const calls = updateSessionRules.mock.calls
    expect(calls[0][0].removeRuleIds).toEqual([100, 101])
    expect(calls[1][0].removeRuleIds).toEqual([102, 103])
  })

  it('continues the sequence on a new adapter over the same session store (worker restart)', async () => {
    const first = createChromeBypass()
    await first.open(testUrl)
    await first.open(testUrl)
    await createChromeBypass().open(testUrl)
    const calls = updateSessionRules.mock.calls
    expect(calls[2][0].removeRuleIds).toEqual([104, 105])
  })

  it('restarts at 100 after a cleared store and a new adapter (browser restart)', async () => {
    const first = createChromeBypass()
    await first.open(testUrl)
    sessionStore.clear()
    await createChromeBypass().open(testUrl)
    const calls = updateSessionRules.mock.calls
    expect(calls[1][0].removeRuleIds).toEqual([100, 101])
  })

  it('falls back to 100 when the stored counter is corrupted', async () => {
    sessionStore.set('bypassNextRuleId', 'not a number')
    await createChromeBypass().open(testUrl)
    expect(updateSessionRules.mock.calls[0][0].removeRuleIds).toEqual([100, 101])
  })

  it('serializes concurrent allocations without overlapping IDs', async () => {
    const bypass = createChromeBypass()
    tabsCreate.mockResolvedValueOnce({ id: 10 }).mockResolvedValueOnce({ id: 20 })
    await Promise.all([bypass.open(testUrl), bypass.open(testUrl)])
    const ids = updateSessionRules.mock.calls.flatMap((c) => c[0].removeRuleIds)
    expect([...ids].sort((a, b) => a - b)).toEqual([100, 101, 102, 103])
  })

  it('persists the tab→rule mapping in storage.session', async () => {
    await createChromeBypass().open(testUrl)
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({ 99: [100, 101] })
  })

  it('removes a remembered tab\'s rules via updateSessionRules without addRules', async () => {
    const bypass = createChromeBypass()
    await bypass.open(testUrl)
    await bypass.close(99)
    const update = updateSessionRules.mock.calls[1][0]
    expect(update).toEqual({ removeRuleIds: [100, 101] })
    expect(update).not.toHaveProperty('addRules')
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({})
  })

  it('keeps the mapping intact when the rule removal fails', async () => {
    const bypass = createChromeBypass()
    await bypass.open(testUrl)
    const error = new Error('session rules rejected')
    updateSessionRules.mockRejectedValueOnce(error)
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    await bypass.close(99)
    expect(consoleError).toHaveBeenCalledWith('[docu-kiosk] bypass cleanup failed:', error)

    // The rules still have an owner: a retry can clean them up.
    await bypass.close(99)
    const calls = updateSessionRules.mock.calls
    expect(calls[1]).toEqual([{ removeRuleIds: [100, 101] }])
    expect(calls[2]).toEqual([{ removeRuleIds: [100, 101] }])
  })

  it('serializes concurrent closes without resurrecting entries', async () => {
    const bypass = createChromeBypass()
    tabsCreate.mockResolvedValueOnce({ id: 7 }).mockResolvedValueOnce({ id: 8 })
    await bypass.open(testUrl)
    await bypass.open(testUrl)
    await Promise.all([bypass.close(7), bypass.close(8)])
    // Without chain serialization, one close would write back a map that
    // re-includes the other tab's entry.
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({})
  })

  it('logs a visible error when the dynamic-rules update rejects', async () => {
    const error = new Error('dynamic rules rejected')
    updateDynamicRules.mockRejectedValueOnce(error)
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    await createChromeBypass().installIntercept()
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] failed to install interception rules:',
      error
    )
  })

  it('close is a no-op for an unmapped tab', async () => {
    await createChromeBypass().close(7)
    expect(updateSessionRules).not.toHaveBeenCalled()
  })

  it('keeps other tabs mapped when closing one tab', async () => {
    const bypass = createChromeBypass()
    tabsCreate.mockResolvedValueOnce({ id: 7 }).mockResolvedValueOnce({ id: 8 })
    await bypass.open(testUrl)
    await bypass.open(testUrl)
    await bypass.close(7)
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({ 8: [102, 103] })
  })

  it('keeps the tab→rule mapping across adapter recreation (worker restart)', async () => {
    const first = createChromeBypass()
    await first.open(testUrl)
    await createChromeBypass().close(99)
    expect(updateSessionRules.mock.calls[1]).toEqual([{ removeRuleIds: [100, 101] }])
  })

  it('clears the tab→rule mapping with the store (browser restart)', async () => {
    const first = createChromeBypass()
    await first.open(testUrl)
    sessionStore.clear()
    await createChromeBypass().close(99)
    expect(updateSessionRules).toHaveBeenCalledTimes(1)
  })

  it('tolerates a corrupted stored tab map', async () => {
    sessionStore.set('bypassTabRuleIds', 'garbage')
    const bypass = createChromeBypass()
    await expect(bypass.close(7)).resolves.toBeUndefined()
    await bypass.open(testUrl)
    await bypass.close(99)
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({})
  })
})
