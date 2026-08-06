import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'

// The background module wires up Chrome APIs at import time, so stub a full
// chrome mock BEFORE importing it. Vitest isolates modules per test file, so
// this does not affect the pure-logic tests in background.test.ts.
const updateDynamicRules = vi.fn()
const getURL = vi.fn()
const tabsCreate = vi.fn()
const actionOnClicked = vi.fn()
const onMessageAddListener = vi.fn()
const webNavOnBeforeNavigate = vi.fn()
const webNavOnCompleted = vi.fn()
const webNavOnCompletedRemove = vi.fn()
const storageManagedGet = vi.fn()
const storageLocalGet = vi.fn()

vi.stubGlobal('chrome', {
  declarativeNetRequest: {
    updateDynamicRules,
  },
  runtime: {
    getURL,
    onMessage: { addListener: onMessageAddListener }
  },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: {
    create: tabsCreate,
  },
  webNavigation: {
    onBeforeNavigate: { addListener: webNavOnBeforeNavigate },
    onCompleted: { addListener: webNavOnCompleted, removeListener: webNavOnCompletedRemove },
  },
  storage: {
    managed: { get: storageManagedGet },
    local: { get: storageLocalGet }
  }
})

const {
  SIGNING_URL_FILTERS,
  installRules,
  handleBypass
} = await import('./background')
const { getConfig } = await import('./config')

// Capture the startup install + listeners registered at import time, since
// beforeEach clears the mocks before each test.
const startupCall = updateDynamicRules.mock.calls[0]?.[0] as
  | { removeRuleIds: number[]; addRules: chrome.declarativeNetRequest.Rule[] }
  | undefined
const onClickedListener = actionOnClicked.mock.calls[0]?.[0] as (() => void) | undefined
const navListener = webNavOnBeforeNavigate.mock.calls[0]?.[0] as
  | ((details: { frameId: number; url: string }) => void)
  | undefined
const navFilter = webNavOnBeforeNavigate.mock.calls[0]?.[1] as
  | { url: { hostSuffix: string }[] }
  | undefined
const onMessageListener = onMessageAddListener.mock.calls[0]?.[0] as
  | ((
    message: unknown,
    sender: unknown,
    sendResponse: (response?: unknown) => void
  ) => boolean | undefined)
  | undefined

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  tabsCreate.mockResolvedValue({ id: 99 })
  storageManagedGet.mockResolvedValue({})
  storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.internal' })
  // Re-register the startup listener mocks since we cleared them
  actionOnClicked.mockImplementation((fn: () => void) => { actionOnClicked.mock.calls.push([fn]) })
  webNavOnBeforeNavigate.mockImplementation((fn: () => void, filter: unknown) => {
    webNavOnBeforeNavigate.mock.calls.push([fn, filter])
  })
  webNavOnCompleted.mockImplementation((fn: () => void) => {
    webNavOnCompleted.mock.calls.push([fn])
  })
  onMessageAddListener.mockImplementation((fn: () => void) => {
    onMessageAddListener.mock.calls.push([fn])
  })
})

afterAll(() => vi.unstubAllGlobals())

describe('rule installation', () => {
  it('installs one short regex rule per signing pattern at startup', () => {
    expect(startupCall).toBeDefined()
    expect(startupCall!.removeRuleIds).toEqual([1, 2, 3, 4])
    expect(startupCall!.addRules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(startupCall!.addRules.map((r) => r.condition.regexFilter)).toEqual(SIGNING_URL_FILTERS)
  })

  it('redirects main_frame requests to the intercepted page, preserving the original URL', async () => {
    await installRules()
    const call = updateDynamicRules.mock.calls[updateDynamicRules.mock.calls.length - 1]
    const rules = call[0].addRules as chrome.declarativeNetRequest.Rule[]
    for (const rule of rules) {
      expect(rule.action.type).toBe('redirect')
      expect(rule.condition.resourceTypes).toEqual(['main_frame'])
      const sub = String(rule.action.redirect?.regexSubstitution)
      expect(sub).toMatch(/^chrome-extension:\/\/testid\/src\/intercepted\/index\.html#url=/)
      expect(sub).toMatch(/#url=\\0$/)
    }
  })

  it('logs a visible error if the browser rejects the rules (e.g. oversized regex)', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    updateDynamicRules.mockRejectedValueOnce(new Error('Rule with id 1 was skipped'))
    await installRules()
    expect(consoleError).toHaveBeenCalledWith(
      '[docu-kiosk] failed to install interception rules:',
      expect.any(Error)
    )
  })
})

describe('toolbar action', () => {
  it('opens the settings page in a tab when the toolbar icon is clicked', () => {
    expect(onClickedListener).toBeTypeOf('function')
    onClickedListener!()
    expect(tabsCreate).toHaveBeenCalledWith({ url: 'chrome-extension://testid/src/options/index.html' })
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
    expect(navFilter).toEqual({ url: [{ hostSuffix: 'docusign.net' }, { hostSuffix: 'docusign.com' }] })
  })
})

describe('config', () => {
  it('falls back to the local brokerUrl when managed storage is empty', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })

  it('prefers the managed (policy-pushed) brokerUrl', async () => {
    storageManagedGet.mockResolvedValue({ brokerUrl: 'https://broker.managed' })
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.managed')
  })
})

describe('bypass', () => {
  const testUrl = 'https://demo.docusign.net/Signing/StartInSession.aspx?t=abc'

  it('removes intercept rules, creates the tab, and restores rules after onCompleted fires', async () => {
    await handleBypass(testUrl)

    // 1. Intercept rules removed
    expect(updateDynamicRules).toHaveBeenCalledWith({ removeRuleIds: [1, 2, 3, 4] })

    // 2. Tab created with the URL, active
    expect(tabsCreate).toHaveBeenCalledWith({ url: testUrl, active: true })

    // 3. onCompleted listener registered
    const completedCalls = webNavOnCompleted.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    expect(completedCalls.length).toBeGreaterThanOrEqual(1)
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // 4. Simulate main-frame completion → rules restored immediately
    completedListener({ tabId: 99, frameId: 0 })
    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls.length).toBeGreaterThanOrEqual(1)
  })

  it('restores rules after the safety timeout even if onCompleted never fires', async () => {
    await handleBypass(testUrl)

    // Fast-forward past the 15-second safety window
    vi.advanceTimersByTime(15_001)

    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls.length).toBeGreaterThanOrEqual(1)
  })

  it('restores rules only once (idempotent) when both onCompleted and timeout fire', async () => {
    await handleBypass(testUrl)

    // Capture the completed listener
    const completedCalls = webNavOnCompleted.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // Fire onCompleted first
    completedListener({ tabId: 99, frameId: 0 })

    // Then let the timeout fire
    vi.advanceTimersByTime(15_001)

    // Rules should be re-installed exactly once
    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls).toHaveLength(1)
  })

  it('restores rules immediately if tab creation fails', async () => {
    tabsCreate.mockRejectedValueOnce(new Error('too many tabs'))

    await expect(handleBypass(testUrl)).rejects.toThrow('too many tabs')

    // Rules should have been restored despite the tab creation error
    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls.length).toBeGreaterThanOrEqual(1)
  })

  it('handles bypass via the runtime.onMessage listener', async () => {
    expect(onMessageListener).toBeTypeOf('function')

    const sendResponse = vi.fn()
    const result = onMessageListener!(
      { type: 'bypass', url: testUrl },
      {},
      sendResponse
    )

    // Should return true to keep the channel open for async response
    expect(result).toBe(true)

    // Wait for the async handler chain to complete
    await vi.waitFor(() => {
      expect(tabsCreate).toHaveBeenCalled()
      expect(sendResponse).toHaveBeenCalled()
    })
  })

  it('sends an error response when bypass fails', async () => {
    updateDynamicRules.mockRejectedValueOnce(new Error('DNR API unavailable'))

    const sendResponse = vi.fn()
    onMessageListener!(
      { type: 'bypass', url: testUrl },
      {},
      sendResponse
    )

    // Wait for the async handler to fail and send the error response
    await vi.waitFor(() => {
      expect(sendResponse).toHaveBeenCalledWith({ error: expect.stringContaining('DNR') })
    })
  })

  it('ignores messages that are not bypass requests', () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!({ type: 'other' }, {}, sendResponse)
    expect(result).toBeUndefined()
    expect(sendResponse).not.toHaveBeenCalled()
  })

  it('only triggers restore for the bypass tab, not other tabs', async () => {
    await handleBypass(testUrl)

    const completedCalls = webNavOnCompleted.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // Navigation completed in a DIFFERENT tab (50) — should NOT restore rules
    completedListener({ tabId: 50, frameId: 0 })

    // No restore calls from onCompleted yet
    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls).toHaveLength(0)

    // Now the correct tab completes — should restore
    completedListener({ tabId: 99, frameId: 0 })
    const addCalls2 = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 4
    )
    expect(addCalls2).toHaveLength(1)
  })
})
