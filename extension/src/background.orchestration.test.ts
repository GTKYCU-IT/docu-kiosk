import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'
import { BYPASS_RULE_START_ID } from './dnr-port'
import { createFakeDnrPort } from './dnr-port.fake'
import type { FakeDnrPort } from './dnr-port.fake'
import type * as DnrPortModule from './dnr-port'
import type * as BackgroundModule from './background'

// The background module wires up its DNR port and Chrome listeners at import
// time. The port factory is mocked to return the fake (via the hoisted holder,
// since the mock factory cannot close over test-file bindings), and chrome is
// stubbed WITHOUT declarativeNetRequest and WITHOUT storage.session — the
// extension must not touch those APIs directly anymore. Vitest isolates
// modules per test file, so this does not affect background.test.ts.
const holder = vi.hoisted(() => ({ fake: null as unknown as FakeDnrPort }))

vi.mock('./dnr-port', async (importOriginal) => ({
  // dnr-port.fake.ts imports BYPASS_RULE_START_ID as a runtime value, so the
  // mock must keep the real module's exports — the spread does that.
  ...(await importOriginal<typeof DnrPortModule>()),
  createChromeDnrPort: () => holder.fake,
}))

const getURL = vi.fn()
const onMessageAddListener = vi.fn()
const actionOnClicked = vi.fn()
const tabsCreate = vi.fn()
const tabsUpdate = vi.fn()
const tabsRemove = vi.fn()
const onRemovedAddListener = vi.fn()
const webNavOnBeforeNavigate = vi.fn()

vi.stubGlobal('chrome', {
  runtime: {
    getURL,
    onMessage: { addListener: onMessageAddListener },
  },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: {
    create: tabsCreate,
    update: tabsUpdate,
    remove: tabsRemove,
    onRemoved: { addListener: onRemovedAddListener },
  },
  webNavigation: { onBeforeNavigate: { addListener: webNavOnBeforeNavigate } },
})

// `mod` is loaded with `await import()` (not a static import) deliberately:
// the service-worker-restart tests re-execute the module top-level via
// vi.resetModules(), which only a dynamic import can do — static imports
// cannot be reloaded. The type comes from the top-level type-only import.
let mod: typeof BackgroundModule
let onClickedListener: (() => void) | undefined
let navListener: ((details: { frameId: number; url: string }) => void) | undefined
let navFilter: { url: { hostSuffix: string }[] } | undefined
let onMessageListener:
  | ((message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean | undefined)
  | undefined
let onRemovedListener: ((tabId: number) => void) | undefined

const testUrl = 'https://demo.docusign.net/Signing/StartInSession.aspx?t=abc'

beforeEach(async () => {
  vi.clearAllMocks()
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  tabsCreate.mockResolvedValue({ id: 99 })
  tabsUpdate.mockResolvedValue(undefined)

  // Fresh fake port and fresh module instance per test, so the startup
  // install and listener wiring run against the fake and the chrome stub.
  holder.fake = createFakeDnrPort()
  vi.resetModules()
  mod = await import('./background')

  // Listeners are registered exactly once per import — capture them now.
  onClickedListener = actionOnClicked.mock.calls[0]?.[0]
  navListener = webNavOnBeforeNavigate.mock.calls[0]?.[0]
  navFilter = webNavOnBeforeNavigate.mock.calls[0]?.[1]
  onMessageListener = onMessageAddListener.mock.calls[0]?.[0]
  onRemovedListener = onRemovedAddListener.mock.calls[0]?.[0]
})

afterAll(() => vi.unstubAllGlobals())

/** IDs of the session rules currently scoped to `tabId` on the fake port. */
function rulesForTab(tabId: number): number[] {
  return [...holder.fake.sessionRules.values()]
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

describe('startup rule installation', () => {
  it('installs the four intercept rules onto a pristine port at startup', () => {
    expect(holder.fake.dynamicRules.size).toBe(4)
    const rules = [...holder.fake.dynamicRules.values()]
    expect(rules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(rules.map((r) => r.condition.regexFilter)).toEqual(mod.SIGNING_URL_FILTERS)
  })

  it('replaces stale intercept rules from a prior worker run at startup', async () => {
    // Dynamic-scope rules survive a service-worker restart, so a prior run's
    // intercept rules (IDs 1-4) can still be installed when this worker boots.
    // The startup install must sweep them before adding the fresh set.
    const stalePort = createFakeDnrPort()
    for (let id = 1; id <= 4; id++) stalePort.dynamicRules.set(id, makeStaleRule(id))
    holder.fake = stalePort
    vi.resetModules()
    mod = await import('./background')

    expect(holder.fake.dynamicRules.size).toBe(4)
    const rules = [...holder.fake.dynamicRules.values()]
    expect(rules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(rules.map((r) => r.condition.regexFilter)).toEqual(mod.SIGNING_URL_FILTERS)
    expect(rules.some((r) => r.condition.regexFilter === '^stale$')).toBe(false)
  })

  it('logs a visible error when the port rejects the rules', async () => {
    vi.spyOn(holder.fake, 'installInterceptRules').mockRejectedValueOnce(
      new Error('Rule with id 1 was skipped')
    )
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    await mod.installRules()
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] failed to install interception rules:',
      expect.any(Error)
    )
  })
})

describe('toolbar action', () => {
  it('opens the settings page when the toolbar icon is clicked', () => {
    onClickedListener!()
    expect(tabsCreate).toHaveBeenCalledWith({
      url: 'chrome-extension://testid/src/options/index.html',
    })
  })
})

describe('debug logger', () => {
  it('logs main-frame navigations to docusign hosts', () => {
    const log = vi.spyOn(console, 'log').mockImplementation(() => {})
    navListener!({ frameId: 0, url: 'https://www.docusign.net/Signing/MTRedeem/v1/abc?slt=x' })
    expect(log).toHaveBeenCalledWith(
      '[docu-kiosk] docusign navigation:',
      'https://www.docusign.net/Signing/MTRedeem/v1/abc?slt=x'
    )
  })

  it('ignores sub-frame navigations', () => {
    const log = vi.spyOn(console, 'log').mockImplementation(() => {})
    navListener!({ frameId: 1, url: 'https://www.docusign.net/Signing/x' })
    expect(log).not.toHaveBeenCalled()
  })

  it('is scoped to docusign hosts via the url filter', () => {
    expect(navFilter).toEqual({
      url: [{ hostSuffix: 'docusign.net' }, { hostSuffix: 'docusign.com' }],
    })
  })
})

describe('bypass', () => {
  it('creates a blank tab, installs session allow rules, and navigates to the URL', async () => {
    await mod.handleBypass(testUrl)

    expect(tabsCreate).toHaveBeenCalledWith({ url: 'about:blank', active: false })
    expect(holder.fake.sessionRules.size).toBe(2)
    const rules = [...holder.fake.sessionRules.values()]
    expect(rules.map((r) => r.id)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rules.every((r) => r.action.type === 'allow')).toBe(true)
    expect(rules.map((r) => r.condition.tabIds)).toEqual([[99], [99]])

    expect(tabsUpdate).toHaveBeenCalledWith(99, { url: testUrl, active: true })
  })

  it('handles bypass via the runtime.onMessage listener', async () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!({ type: 'bypass', url: testUrl }, {}, sendResponse)

    // Returns true to keep the message channel open for the async response.
    expect(result).toBe(true)
    await vi.waitFor(() => {
      expect(sendResponse).toHaveBeenCalled()
    })
    expect(sendResponse).toHaveBeenCalledWith()
  })

  it('sends an error response when the bypass fails', async () => {
    tabsCreate.mockRejectedValueOnce(new Error('quota exhausted'))

    const sendResponse = vi.fn()
    onMessageListener!({ type: 'bypass', url: testUrl }, {}, sendResponse)

    await vi.waitFor(() => {
      expect(sendResponse).toHaveBeenCalledWith({ error: expect.stringContaining('quota') })
    })
  })

  it('ignores messages that are not bypass requests', () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!({ type: 'other' }, {}, sendResponse)
    expect(result).toBeUndefined()
    expect(sendResponse).not.toHaveBeenCalled()
  })
})

describe('bypass tab cleanup', () => {
  it("removes the tab's session rules when the bypass tab closes", async () => {
    await mod.handleBypass(testUrl)
    onRemovedListener!(99)
    // Cleanup is async (persisted mapping lookup) and fire-and-forget from
    // the listener — wait for it to settle.
    await vi.waitFor(() => {
      expect(holder.fake.sessionRules.size).toBe(0)
    })
    expect(holder.fake.tabRuleIds.size).toBe(0)
  })

  it('is a no-op for unknown tab ids', () => {
    onRemovedListener!(999)
    expect(holder.fake.sessionRules.size).toBe(0)
  })
})

describe('multi-tab bypass', () => {
  it('allocates distinct rule IDs per tab and removes only the closed tab rules', async () => {
    tabsCreate
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await mod.handleBypass('https://demo.docusign.net/Signing/a')
    await mod.handleBypass('https://demo.docusign.net/Signing/b')

    expect(holder.fake.sessionRules.size).toBe(4)
    expect([...holder.fake.sessionRules.keys()]).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
    expect(rulesForTab(10)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rulesForTab(20)).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])

    onRemovedListener!(10)
    await vi.waitFor(() => {
      expect([...holder.fake.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])
    })
    expect([...holder.fake.tabRuleIds.keys()]).toEqual([20])
  })
})

describe('service-worker and browser restarts', () => {
  it('continues rule IDs across service-worker restarts on the same port', async () => {
    tabsCreate.mockResolvedValue({ id: 30 })
    await mod.handleBypass('https://demo.docusign.net/Signing/a')
    expect([...holder.fake.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])

    // Service-worker restart: fresh module instance, but the SAME fake port —
    // its session rules and ID counter outlive the worker.
    vi.resetModules()
    mod = await import('./background')
    tabsCreate.mockResolvedValue({ id: 31 })
    await mod.handleBypass('https://demo.docusign.net/Signing/b')

    expect(holder.fake.sessionRules.size).toBe(4)
    expect([...holder.fake.sessionRules.keys()]).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3,
    ])
  })

  it('restarts rule IDs at BYPASS_RULE_START_ID after a browser restart', async () => {
    await mod.handleBypass(testUrl)
    expect([...holder.fake.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])

    // Browser restart: fresh port (session rules and counter cleared) and a
    // fresh module instance.
    holder.fake = createFakeDnrPort()
    vi.resetModules()
    mod = await import('./background')
    tabsCreate.mockResolvedValue({ id: 40 })
    await mod.handleBypass('https://demo.docusign.net/Signing/b')

    expect([...holder.fake.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })

  it('sweeps stale colliding rules instead of duplicating them', async () => {
    await mod.handleBypass(testUrl)
    expect(holder.fake.sessionRules.size).toBe(2)

    // Counter drifts back to BYPASS_RULE_START_ID (e.g. the session counter
    // was cleared) while the session rules from the earlier bypass are still
    // installed.
    holder.fake.nextBypassId = BYPASS_RULE_START_ID
    tabsCreate.mockResolvedValue({ id: 60 })
    await mod.handleBypass('https://demo.docusign.net/Signing/c')

    // Sweep-then-add replaced the stale rules — no "Rule with id N does not
    // have a unique ID" rejection, and no duplicates.
    expect(holder.fake.sessionRules.size).toBe(2)
    expect([...holder.fake.sessionRules.keys()]).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
    expect(rulesForTab(60)).toEqual([BYPASS_RULE_START_ID, BYPASS_RULE_START_ID + 1])
  })

  it("removes a closed tab's rules after a service-worker restart", async () => {
    tabsCreate.mockResolvedValue({ id: 30 })
    await mod.handleBypass('https://demo.docusign.net/Signing/a')
    expect(holder.fake.sessionRules.size).toBe(2)
    expect(holder.fake.tabRuleIds.get(30)).toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1,
    ])

    // Service-worker restart: fresh module instance, but the SAME fake port —
    // its session rules and persisted tab→rule map outlive the worker. The
    // fresh module registered its own listeners; capture the new onRemoved.
    vi.resetModules()
    mod = await import('./background')
    onRemovedListener = onRemovedAddListener.mock.calls[onRemovedAddListener.mock.calls.length - 1]?.[0]

    onRemovedListener!(30)
    await vi.waitFor(() => {
      expect(holder.fake.sessionRules.size).toBe(0)
    })
    expect(holder.fake.tabRuleIds.size).toBe(0)
  })

  it("keeps other tabs' rules when one tab closes after a service-worker restart", async () => {
    tabsCreate
      .mockResolvedValueOnce({ id: 30 })
      .mockResolvedValueOnce({ id: 31 })
    await mod.handleBypass('https://demo.docusign.net/Signing/a')
    await mod.handleBypass('https://demo.docusign.net/Signing/b')

    vi.resetModules()
    mod = await import('./background')
    onRemovedListener = onRemovedAddListener.mock.calls[onRemovedAddListener.mock.calls.length - 1]?.[0]

    onRemovedListener!(30)
    await vi.waitFor(() => {
      expect(holder.fake.sessionRules.size).toBe(2)
    })
    expect(rulesForTab(31)).toEqual([BYPASS_RULE_START_ID + 2, BYPASS_RULE_START_ID + 3])
    expect([...holder.fake.tabRuleIds.keys()]).toEqual([31])
  })

  it('starts with an empty tab→rule map after a browser restart', async () => {
    await mod.handleBypass(testUrl)
    expect(holder.fake.tabRuleIds.size).toBe(1)

    // Browser restart: fresh port (session rules, counter, and tab map
    // cleared) and a fresh module instance.
    holder.fake = createFakeDnrPort()
    vi.resetModules()
    mod = await import('./background')
    onRemovedListener = onRemovedAddListener.mock.calls[onRemovedAddListener.mock.calls.length - 1]?.[0]

    expect(holder.fake.tabRuleIds.size).toBe(0)
    onRemovedListener!(99)
    expect(holder.fake.sessionRules.size).toBe(0)
  })
})

describe('bypass failure cleanup', () => {
  it('removes the orphan blank tab when the bypass install fails', async () => {
    vi.spyOn(holder.fake, 'addBypassRules').mockRejectedValueOnce(new Error('boom'))

    await expect(mod.handleBypass(testUrl)).rejects.toThrow('boom')
    expect(tabsRemove).toHaveBeenCalledWith(99)
  })

  it('still navigates and logs when persisting the tab mapping fails', async () => {
    vi.spyOn(holder.fake, 'rememberBypassTab').mockRejectedValueOnce(new Error('storage full'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    await mod.handleBypass(testUrl)

    // The rules are installed; a persistence failure must not fail the bypass.
    expect(tabsUpdate).toHaveBeenCalledWith(99, { url: testUrl, active: true })
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] bypass state persist failed:',
      expect.any(Error)
    )
  })

  it('logs a failed tab-close cleanup instead of rejecting unhandled', async () => {
    await mod.handleBypass(testUrl)
    vi.spyOn(holder.fake, 'forgetBypassTab').mockRejectedValueOnce(new Error('storage full'))
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})

    onRemovedListener!(99)
    await vi.waitFor(() => {
      expect(consoleError).toHaveBeenCalledWith(
        '[docu-kiosk] bypass cleanup failed:',
        expect.any(Error)
      )
    })
    // Nothing was torn down on failure — rules and mapping stay consistent.
    expect(holder.fake.sessionRules.size).toBe(2)
    expect(holder.fake.tabRuleIds.size).toBe(1)
  })
})
