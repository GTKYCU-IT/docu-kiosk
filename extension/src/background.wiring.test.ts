import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'

// The background module wires up Chrome APIs at import time, so stub a full
// chrome mock BEFORE importing it. Vitest isolates modules per test file, so
// this does not affect the pure-logic tests in background.test.ts.
const updateDynamicRules = vi.fn()
const getDynamicRules = vi.fn()
const getURL = vi.fn()
const tabsCreate = vi.fn()
const actionOnClicked = vi.fn()
const onMessageAddListener = vi.fn()
const onRemovedAddListener = vi.fn()
const webNavOnBeforeNavigate = vi.fn()
const webNavOnCompletedAdd = vi.fn()
const webNavOnCompletedRemove = vi.fn()
const storageManagedGet = vi.fn()
const storageLocalGet = vi.fn()

vi.stubGlobal('chrome', {
  declarativeNetRequest: {
    updateDynamicRules,
    getDynamicRules
  },
  runtime: {
    getURL,
    onMessage: { addListener: onMessageAddListener }
  },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: {
    create: tabsCreate,
    onRemoved: { addListener: onRemovedAddListener }
  },
  webNavigation: {
    onBeforeNavigate: { addListener: webNavOnBeforeNavigate },
    onCompleted: { addListener: webNavOnCompletedAdd, removeListener: webNavOnCompletedRemove },
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
const onRemovedListener = onRemovedAddListener.mock.calls[0]?.[0] as
  | ((tabId: number) => void)
  | undefined

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  getDynamicRules.mockResolvedValue([])
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  tabsCreate.mockResolvedValue({ id: 99 })
  storageManagedGet.mockResolvedValue({})
  storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.internal' })
  // Re-register the startup listener mocks since we cleared them
  actionOnClicked.mockImplementation((fn: () => void) => { actionOnClicked.mock.calls.push([fn]) })
  webNavOnBeforeNavigate.mockImplementation((fn: () => void, filter: unknown) => {
    webNavOnBeforeNavigate.mock.calls.push([fn, filter])
  })
  webNavOnCompletedAdd.mockImplementation((fn: () => void) => {
    webNavOnCompletedAdd.mock.calls.push([fn])
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

  it('sweeps stale bypass rules (IDs >= 100) on startup', async () => {
    getDynamicRules.mockResolvedValue([
      { id: 100, priority: 100, action: { type: 'allow' }, condition: {} },
      { id: 101, priority: 100, action: { type: 'allow' }, condition: {} }
    ] as chrome.declarativeNetRequest.Rule[])
    await installRules()
    const calls = updateDynamicRules.mock.calls
    const lastCall = calls[calls.length - 1]
    expect(lastCall).toBeDefined()
    expect(lastCall[0].removeRuleIds).toContain(100)
    expect(lastCall[0].removeRuleIds).toContain(101)
  })

  it('does not sweep when there are no stale bypass rules', async () => {
    getDynamicRules.mockResolvedValue([])
    await installRules()
    const removalCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.some((id: number) => id >= 100)
    )
    expect(removalCalls).toHaveLength(0)
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

  it('removes intercept rules, creates the tab, then installs allow + intercept rules after onCompleted', async () => {
    await handleBypass(testUrl)

    // 1. Intercept rules removed
    expect(updateDynamicRules).toHaveBeenCalledWith({ removeRuleIds: [1, 2, 3, 4] })

    // 2. Tab created with the URL, active
    expect(tabsCreate).toHaveBeenCalledWith({ url: testUrl, active: true })

    // 3. onCompleted listener registered
    const completedCalls = webNavOnCompletedAdd.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    expect(completedCalls.length).toBeGreaterThanOrEqual(1)
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // 4. Simulate main-frame completion → allow + intercept rules installed
    completedListener({ tabId: 99, frameId: 0 })

    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length > 0
    )
    expect(addCalls.length).toBeGreaterThanOrEqual(1)
    const added = addCalls[addCalls.length - 1][0].addRules as chrome.declarativeNetRequest.Rule[]

    // Should contain 2 allow rules (ids 100, 101) + 4 intercept rules (ids 1-4)
    expect(added).toHaveLength(6)
    const allowRules = added.filter((r) => r.action.type === 'allow')
    expect(allowRules).toHaveLength(2)
    expect(allowRules[0].priority).toBe(100)
    expect(allowRules[0].condition.tabIds).toEqual([99])
    const interceptRules = added.filter((r) => r.action.type === 'redirect')
    expect(interceptRules).toHaveLength(4)
  })

  it('installs allow + intercept rules after the safety timeout if onCompleted never fires', async () => {
    await handleBypass(testUrl)

    // Fast-forward past the 15-second safety window
    vi.advanceTimersByTime(15_001)

    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length > 0
    )
    expect(addCalls.length).toBeGreaterThanOrEqual(1)
    const added = addCalls[addCalls.length - 1][0].addRules as chrome.declarativeNetRequest.Rule[]
    expect(added).toHaveLength(6)
    expect(added.filter((r) => r.action.type === 'allow')).toHaveLength(2)
    expect(added.filter((r) => r.action.type === 'redirect')).toHaveLength(4)
  })

  it('only installs rules once when both onCompleted and timeout fire', async () => {
    await handleBypass(testUrl)

    // Capture the completed listener
    const completedCalls = webNavOnCompletedAdd.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // Fire onCompleted first
    completedListener({ tabId: 99, frameId: 0 })

    // Then let the timeout fire
    vi.advanceTimersByTime(15_001)

    // Rules should be installed exactly once
    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length > 0
    )
    expect(addCalls).toHaveLength(1)
  })

  it('restores intercept rules immediately if tab creation fails', async () => {
    tabsCreate.mockRejectedValueOnce(new Error('too many tabs'))

    await expect(handleBypass(testUrl)).rejects.toThrow('too many tabs')

    // Intercept rules should be restored (the addRules call in the catch block)
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

  it('only triggers finish for the bypass tab, not other tabs', async () => {
    await handleBypass(testUrl)

    const completedCalls = webNavOnCompletedAdd.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    const completedListener = completedCalls[completedCalls.length - 1][0] as
      (details: { tabId: number; frameId: number }) => void

    // Navigation completed in a DIFFERENT tab — should NOT install rules
    completedListener({ tabId: 50, frameId: 0 })

    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length > 0
    )
    expect(addCalls).toHaveLength(0)

    // Now the correct tab completes — should install rules
    completedListener({ tabId: 99, frameId: 0 })
    const addCalls2 = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length > 0
    )
    expect(addCalls2).toHaveLength(1)
  })
})

describe('bypass tab cleanup', () => {
  const testUrl = 'https://demo.docusign.net/Signing/abc'

  it('removes bypass allow rules when the bypass tab closes', async () => {
    await handleBypass(testUrl)

    // Trigger onCompleted so the allow rule is installed
    const completedCalls = webNavOnCompletedAdd.mock.calls.filter(
      (c) => typeof c[0] === 'function'
    )
    completedCalls[completedCalls.length - 1][0]({ tabId: 99, frameId: 0 })

    // Simulate tab close
    onRemovedListener!(99)

    const removalCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(removalCalls).toHaveLength(1)
    // Two rule IDs are removed (one per DocuSign host); exact IDs depend on
    // the shared module-level counter so only verify count.
    expect(removalCalls[0][0].removeRuleIds).toHaveLength(2)
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
    // Trigger onCompleted for first tab
    let calls = webNavOnCompletedAdd.mock.calls
      .filter((c: unknown[]) => typeof c[0] === 'function')
    calls[calls.length - 1]![0]({ tabId: 10, frameId: 0 })

    await handleBypass('https://demo.docusign.net/Signing/b')
    // Trigger onCompleted for second tab
    calls = webNavOnCompletedAdd.mock.calls
      .filter((c: unknown[]) => typeof c[0] === 'function')
    calls[calls.length - 1]![0]({ tabId: 20, frameId: 0 })

    const addCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].addRules?.length === 6
    )
    expect(addCalls).toHaveLength(2)

    const firstAllowIds = (addCalls[0][0].addRules as chrome.declarativeNetRequest.Rule[])
      .filter((r) => r.action.type === 'allow')
      .map((r) => r.id)
    const secondAllowIds = (addCalls[1][0].addRules as chrome.declarativeNetRequest.Rule[])
      .filter((r) => r.action.type === 'allow')
      .map((r) => r.id)

    // IDs should not overlap
    const allIds = [...firstAllowIds, ...secondAllowIds]
    expect(new Set(allIds).size).toBe(allIds.length)
  })

  it('removes only the closed tab rules, leaving the other tab rules intact', async () => {
    tabsCreate
      .mockResolvedValueOnce({ id: 10 })
      .mockResolvedValueOnce({ id: 20 })

    await handleBypass('https://demo.docusign.net/Signing/a')
    let c2 = webNavOnCompletedAdd.mock.calls
      .filter((c: unknown[]) => typeof c[0] === 'function')
    c2[c2.length - 1]![0]({ tabId: 10, frameId: 0 })

    await handleBypass('https://demo.docusign.net/Signing/b')
    c2 = webNavOnCompletedAdd.mock.calls
      .filter((c: unknown[]) => typeof c[0] === 'function')
    c2[c2.length - 1]![0]({ tabId: 20, frameId: 0 })

    // Close tab 10
    onRemovedListener!(10)

    const removalCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(removalCalls).toHaveLength(1)

    // Close tab 20
    onRemovedListener!(20)
    const allRemovalCalls = updateDynamicRules.mock.calls.filter(
      (c) => c[0].removeRuleIds?.length === 2
    )
    expect(allRemovalCalls).toHaveLength(2)
    expect(allRemovalCalls[0][0].removeRuleIds).not.toEqual(
      allRemovalCalls[1][0].removeRuleIds)
  })
})
