import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'
import type { Bypass } from './bypass'
import { registerBackgroundListeners } from './background'

// The background module is imported once, statically: it has no import-time
// side effects (verified below), so there is no module re-import to simulate.
// The router is exercised against a direct fake of the three-method Bypass
// interface — no vi.mock, no hoisted holders, no resetModules, no dynamic
// import. chrome is stubbed WITHOUT declarativeNetRequest and WITHOUT
// storage.session — the extension must not touch those APIs directly anymore.
// Rule/filter and lifecycle behavior (blank tab, rule install, persisted
// mapping, sweeps) lives in bypass.test.ts against the real bypass module.
const getURL = vi.fn()
const onMessageAddListener = vi.fn()
const actionOnClicked = vi.fn()
const tabsCreate = vi.fn()
const onRemovedAddListener = vi.fn()
const webNavOnBeforeNavigate = vi.fn()

const stubChrome = {
  runtime: {
    getURL,
    onMessage: { addListener: onMessageAddListener },
  },
  action: { onClicked: { addListener: actionOnClicked } },
  tabs: {
    create: tabsCreate,
    onRemoved: { addListener: onRemovedAddListener },
  },
  webNavigation: { onBeforeNavigate: { addListener: webNavOnBeforeNavigate } },
} as unknown as typeof chrome

vi.stubGlobal('chrome', stubChrome)

// The injected bypass: the router may call open/close (and nothing else) and
// must never construct or install one itself. installIntercept is present
// because it is part of the interface, but only background-main.ts invokes it.
const open = vi.fn()
const close = vi.fn()
const installIntercept = vi.fn()
const bypass: Bypass = { open, close, installIntercept }

let onClickedListener: (() => void) | undefined
let navListener: ((details: { frameId: number; url: string }) => void) | undefined
let navFilter: { url: { hostSuffix: string }[] } | undefined
let onMessageListener:
  | ((message: unknown, sender: unknown, sendResponse: (response?: unknown) => void) => boolean | undefined)
  | undefined
let onRemovedListener: ((tabId: number) => void) | undefined

const testUrl = 'https://demo.docusign.net/Signing/StartInSession.aspx?t=abc'

beforeEach(() => {
  vi.clearAllMocks()
  getURL.mockImplementation((p: string) => `chrome-extension://testid/${p}`)
  open.mockResolvedValue(1)
  close.mockResolvedValue(undefined)

  // The same wiring the service-worker entry performs, against the fresh fake.
  registerBackgroundListeners(bypass)

  // Listeners are registered exactly once per wiring call — capture them now.
  onClickedListener = actionOnClicked.mock.calls[0]?.[0]
  navListener = webNavOnBeforeNavigate.mock.calls[0]?.[0]
  navFilter = webNavOnBeforeNavigate.mock.calls[0]?.[1]
  onMessageListener = onMessageAddListener.mock.calls[0]?.[0]
  onRemovedListener = onRemovedAddListener.mock.calls[0]?.[0]
})

afterAll(() => vi.unstubAllGlobals())

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

describe('bypass message routing', () => {
  it('delegates a bypass request to bypass.open and answers on the async channel', async () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!({ type: 'bypass', url: testUrl }, {}, sendResponse)

    expect(open).toHaveBeenCalledWith(testUrl)
    // Returns true to keep the message channel open for the async response.
    expect(result).toBe(true)
    expect(sendResponse).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(sendResponse).toHaveBeenCalled()
    })
    expect(sendResponse).toHaveBeenCalledWith()
  })

  it('sends an error response when the bypass fails', async () => {
    open.mockRejectedValueOnce(new Error('quota exhausted'))

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
    expect(open).not.toHaveBeenCalled()
    expect(sendResponse).not.toHaveBeenCalled()
  })

  it('ignores bypass messages whose url is not a string', () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!({ type: 'bypass', url: 42 }, {}, sendResponse)
    expect(result).toBeUndefined()
    expect(open).not.toHaveBeenCalled()
    expect(sendResponse).not.toHaveBeenCalled()
  })

  it('tolerates undefined messages', () => {
    const sendResponse = vi.fn()
    const result = onMessageListener!(undefined, {}, sendResponse)
    expect(result).toBeUndefined()
    expect(open).not.toHaveBeenCalled()
    expect(sendResponse).not.toHaveBeenCalled()
  })
})

describe('tab close routing', () => {
  it('delegates the removed tab id to bypass.close', () => {
    onRemovedListener!(99)
    expect(close).toHaveBeenCalledWith(99)
  })

  it('is fire-and-forget: never routes tab removal through bypass.open', () => {
    onRemovedListener!(99)
    expect(open).not.toHaveBeenCalled()
  })
})

describe('module import side-effect freedom', () => {
  it('importing ./background registers no listeners and calls nothing', () => {
    // The static import at the top of this file evaluated ./background before
    // any test body ran and before chrome was stubbed: any import-time side
    // effect (listener registration, bypass singleton construction, intercept
    // install) would have touched chrome APIs that are absent in the test
    // environment and failed the whole suite at load. No module re-import or
    // reset is possible — or needed — with the static import: the module's
    // only export is the wiring function and all work is delegated to the
    // injected bypass.
    expect(registerBackgroundListeners).toBeTypeOf('function')

    // Registration wires listeners only — it never installs interception or
    // runs a bypass; background-main.ts owns that bootstrap.
    expect(installIntercept).not.toHaveBeenCalled()
    expect(open).not.toHaveBeenCalled()
    expect(close).not.toHaveBeenCalled()
  })
})
