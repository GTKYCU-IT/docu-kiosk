import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'

// The background module wires up Chrome APIs at import time, so stub a full
// chrome mock BEFORE importing it. Vitest isolates modules per test file, so
// this does not affect the pure-logic tests in background.test.ts.
const updateDynamicRules = vi.fn()
const getURL = vi.fn()
const tabsCreate = vi.fn()
const actionOnClicked = vi.fn()
const webNavOnBeforeNavigate = vi.fn()
const storageManagedGet = vi.fn()
const storageLocalGet = vi.fn()

vi.stubGlobal('chrome', {
  declarativeNetRequest: { updateDynamicRules },
  runtime: { getURL },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: { create: tabsCreate },
  webNavigation: { onBeforeNavigate: { addListener: webNavOnBeforeNavigate } },
  storage: {
    managed: { get: storageManagedGet },
    local: { get: storageLocalGet }
  }
})

const { SIGNING_URL_FILTERS, installRules } = await import('./background')
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

beforeEach(() => {
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  storageManagedGet.mockResolvedValue({})
  storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.internal' })
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
