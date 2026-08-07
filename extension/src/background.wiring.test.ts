import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'

// The background module wires up Chrome APIs at import time, so stub a full
// chrome mock BEFORE importing it. Vitest isolates modules per test file, so
// this does not affect the pure-logic tests in background.test.ts.
const updateDynamicRules = vi.fn()
const updateSessionRules = vi.fn()
const getURL = vi.fn()
const tabsCreate = vi.fn()
const tabsUpdate = vi.fn()
const actionOnClicked = vi.fn()
const onMessageAddListener = vi.fn()
const onRemovedAddListener = vi.fn()
const webNavOnBeforeNavigate = vi.fn()
const storageManagedGet = vi.fn()
const storageLocalGet = vi.fn()

vi.stubGlobal('chrome', {
  declarativeNetRequest: {
    updateDynamicRules,
    updateSessionRules,
  },
  runtime: {
    getURL,
    onMessage: { addListener: onMessageAddListener }
  },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: {
    create: tabsCreate,
    update: tabsUpdate,
    onRemoved: { addListener: onRemovedAddListener }
  },
  webNavigation: { onBeforeNavigate: { addListener: webNavOnBeforeNavigate } },
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
const onRemovedListener = onRemovedAddListener.mock.calls[0]?.[0] as
  | ((tabId: number) => void)
  | undefined

beforeEach(() => {
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  updateSessionRules.mockResolvedValue(undefined)
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  tabsCreate.mockResolvedValue({ id: 99 })
  tabsUpdate.mockResolvedValue(undefined)
  storageManagedGet.mockResolvedValue({})
  storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.internal' })
  // Re-register the startup listener mocks since we cleared them
  actionOnClicked.mockImplementation((fn: () => void) => { actionOnClicked.mock.calls.push([fn]) })
  webNavOnBeforeNavigate.mockImplementation((fn: () => void, filter: unknown) => {
    webNavOnBeforeNavigate.mock.calls.push([fn, filter])
  })
  onMessageAddListener.mockImplementation((fn: () => void) => {
    onMessageAddListener.mock.calls.push([fn])
  })
  onRemovedAddListener.mockImplementation((fn: () => void) => {
    onRemovedAddListener.mock.calls.push([fn])
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

  it('strips a trailing slash from the local brokerUrl', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local/' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })

  it('strips trailing slashes from the managed brokerUrl and kioskUrl', async () => {
    storageManagedGet.mockResolvedValue({ brokerUrl: 'https://broker.managed///', kioskUrl: 'https://kiosk.local/' })
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.managed')
    expect(cfg.kioskUrl).toBe('https://kiosk.local')
  })

  it('leaves a slash-less brokerUrl unchanged', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })
})

describe('bypass message handler', () => {
  const testUrl = 'https://demo.docusign.net/Signing/StartInSession.aspx?t=abc'

  it('creates a blank tab, installs session-scoped bypass rules, and navigates to the URL', async () => {
    await handleBypass(testUrl)

    expect(tabsCreate).toHaveBeenCalledWith({ url: 'about:blank', active: false })

    // Bypass rules are session-scoped (support tabIds) — not dynamic
    expect(updateDynamicRules).not.toHaveBeenCalled()
    const rulesCall = updateSessionRules.mock.calls.find(
      (c) => c[0].addRules?.length > 0
    )
    expect(rulesCall).toBeDefined()
    const added = rulesCall![0].addRules as chrome.declarativeNetRequest.Rule[]
    expect(added).toHaveLength(2)
    expect(added[0].action.type).toBe('allow')
    expect(added[0].priority).toBe(100)
    expect(added[0].condition.tabIds).toEqual([99])
    expect(added[1].condition.tabIds).toEqual([99])

    expect(tabsUpdate).toHaveBeenCalledWith(99, { url: testUrl, active: true })
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

    // Wait for the async handler chain (tabs.create → updateSessionRules → tabs.update)
    await vi.waitFor(() => {
      expect(tabsCreate).toHaveBeenCalled()
      expect(sendResponse).toHaveBeenCalled()
    })
  })

  it('sends an error response when bypass fails', async () => {
    tabsCreate.mockRejectedValueOnce(new Error('quota exhausted'))

    const sendResponse = vi.fn()
    onMessageListener!(
      { type: 'bypass', url: testUrl },
      {},
      sendResponse
    )

    // Wait for the async handler to fail and send the error response
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
  const testUrl = 'https://demo.docusign.net/Signing/abc'

  it('removes session-scoped bypass rules when the bypass tab closes', async () => {
    await handleBypass(testUrl)

    // The tab was created with id 99
    onRemovedListener!(99)

    const removalCalls = updateSessionRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(removalCalls).toHaveLength(1)
  })

  it('does not error when an unknown tab is removed', () => {
    onRemovedListener!(999)
    // Should not throw
  })
})

describe('multi-tab bypass', () => {
  it('allocates distinct rule IDs for each bypass tab', async () => {
    tabsCreate
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await handleBypass('https://demo.docusign.net/Signing/a')
    const firstCall = updateSessionRules.mock.calls.find(
      (c) => c[0].addRules?.length === 2
    )
    const firstIds = (firstCall![0].addRules as chrome.declarativeNetRequest.Rule[]).map((r) => r.id)

    await handleBypass('https://demo.docusign.net/Signing/b')
    const addCalls = updateSessionRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 2
    )
    const secondIds = (addCalls[1][0].addRules as chrome.declarativeNetRequest.Rule[]).map((r) => r.id)

    // IDs should not overlap
    const allIds = [...firstIds, ...secondIds]
    expect(new Set(allIds).size).toBe(allIds.length)
  })

  it('removes only the closed tab rules, leaving the other tab rules intact', async () => {
    tabsCreate
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await handleBypass('https://demo.docusign.net/Signing/a')
    await handleBypass('https://demo.docusign.net/Signing/b')

    // Close tab 10
    onRemovedListener!(10)

    const removalCalls = updateSessionRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(removalCalls).toHaveLength(1)

    // Close tab 20
    onRemovedListener!(20)
    const allRemovalCalls = updateSessionRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(allRemovalCalls).toHaveLength(2)
    // The two removal calls should remove different rule ID sets
    expect(allRemovalCalls[0][0].removeRuleIds).not.toEqual(
      allRemovalCalls[1][0].removeRuleIds)
  })
})
