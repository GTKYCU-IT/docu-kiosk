import type { Bypass } from './bypass'
import { BYPASS_MESSAGE_TYPE } from './lib/broker'

/**
 * Wire the service-worker listeners: toolbar action, navigation logging,
 * bypass-tab cleanup, and bypass requests. Importing the module has no side
 * effects — every chrome.* call happens inside a listener registration
 * closure — so tests and other embedders can import it without a Chrome
 * runtime. Called exactly once by the service-worker entry
 * (background-main.ts) with the Chrome-backed bypass module.
 */
export function registerBackgroundListeners(bypass: Bypass): void {
  // Clicking the toolbar icon opens the settings page in a full tab.
  chrome.action.onClicked.addListener(() => {
    void chrome.tabs.create({ url: chrome.runtime.getURL('src/options/index.html') })
  })

  // Debug aid: log every main-frame navigation to a DocuSign host so the
  // original URL is recoverable even when a navigation slips past the
  // interception rule. See it in the service worker console
  // (chrome://extensions → "service worker" → inspect).
  chrome.webNavigation.onBeforeNavigate.addListener(
    (details) => {
      if (details.frameId !== 0) return
      console.log('[docu-kiosk] docusign navigation:', details.url)
    },
    { url: [{ hostSuffix: 'docusign.net' }, { hostSuffix: 'docusign.com' }] }
  )

  // Bypass: when a bypass tab closes, clean up its allow rules and persisted
  // mapping. The bypass module owns the cleanup (session-scoped rules,
  // tab→rule map) and logs its own failures, so a rejection is never
  // expected here — fire-and-forget.
  chrome.tabs.onRemoved.addListener((tabId) => {
    void bypass.close(tabId)
  })

  // Bypass: listen for bypass requests from the intercepted page.
  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (message?.type === BYPASS_MESSAGE_TYPE && typeof message.url === 'string') {
      bypass.open(message.url).then(
        () => sendResponse(),
        (err: unknown) => sendResponse({ error: String(err) })
      )
      return true // keep the channel open for the async response
    }
  })
}
