import { createChromeDnrPort, type DnrPort } from './dnr-port'

/**
 * Signing entry-point URL patterns on DocuSign's hosts.
 *
 * IMPORTANT: Chrome rejects a single regexFilter whose compiled program exceeds
 * the ~2KB per-rule memory limit — an earlier single multi-alternation regex
 * compiled to ~5.7KB and the rule silently never installed. Each pattern below
 * is therefore a separate short rule, sized like the original filter
 * (`^https://[^/]*\.docusign\.(net|com)/.*`, ~35 RE2 instructions) that
 * shipped in production for months.
 */
export const SIGNING_URL_FILTERS: string[] = [
  '^https://[^/]*\\.docusign\\.net/Signing/', // classic embedded signing + email links (MTRedeem, StartInSession, ...)
  '^https://[^/]*\\.docusign\\.net/signing/', // lowercase responsive signing
  '^https://[^/]*\\.docusign\\.net/Member/PowerForm', // PowerForms
  '^https://apps\\.docusign\\.com/authenticate', // new embedded-signing host (2026)
]
const ALL_RULE_IDS = [1, 2, 3, 4]
const BYPASS_HOSTS = ['*://*.docusign.net/*', '*://*.docusign.com/*']

// All declarative-net-request access — intercept install, bypass add/remove,
// bypass-ID allocation, and the persisted tab→rule map — goes through the
// port. Production uses the Chrome-backed adapter; tests inject the fake.
const dnrPort: DnrPort = createChromeDnrPort()

export function buildRules(interceptBaseUrl: string): chrome.declarativeNetRequest.Rule[] {
  const action: chrome.declarativeNetRequest.RuleAction = {
    type: 'redirect',
    redirect: { regexSubstitution: `${interceptBaseUrl}#url=\\0` }
  }
  const mainFrame: chrome.declarativeNetRequest.ResourceType[] = [
    'main_frame' as chrome.declarativeNetRequest.ResourceType
  ]

  return SIGNING_URL_FILTERS.map((regexFilter, i) => ({
    id: i + 1,
    priority: 1,
    action,
    condition: { regexFilter, resourceTypes: mainFrame }
  }))
}

export function buildBypassRules(tabId: number, startId: number): chrome.declarativeNetRequest.Rule[] {
  return BYPASS_HOSTS.map((urlFilter, i) => ({
    id: startId + i,
    priority: 100,
    action: { type: 'allow' as const },
    condition: {
      urlFilter,
      resourceTypes: ['main_frame' as chrome.declarativeNetRequest.ResourceType],
      tabIds: [tabId]
    }
  }))
}

/**
 * Open a DocuSign URL in a new browser tab, bypassing interception.
 *
 * Creates a blank tab to obtain a stable tabId, installs tab-scoped allow
 * rules that out-prioritise the intercept redirect rules, then navigates the
 * tab to the URL.  Every step is awaited so the service worker stays alive
 * until the rules are committed and the navigation has started.
 *
 * The allow rules protect the tab for its entire lifetime — including
 * multi-page signing flows and subsequent same-tab navigation.
 */
export async function handleBypass(url: string) {
  const tab = await chrome.tabs.create({ url: 'about:blank', active: false })
  if (!tab.id) throw new Error('tab has no id')

  try {
    const ruleIds = await dnrPort.allocateBypassRuleIds(2)
    const rules = buildBypassRules(tab.id, ruleIds[0])
    // Sweep-then-add in one atomic call: if the counter ever drifts from the
    // installed rules (e.g. the session counter is cleared on extension
    // reload while session rules persisted), stale rules with these IDs are
    // removed first instead of rejecting the whole update with "Rule with id
    // N does not have a unique ID". Live rules never carry these IDs — the
    // counter is monotonic within a browser session — so this cannot disable
    // another tab's bypass.
    await dnrPort.addBypassRules(rules, ruleIds)
    // Persist the tab→rule mapping with the same lifetime as the session
    // rules: the worker restarts long before a bypass tab closes, so cleanup
    // cannot rely on module memory. A persistence failure leaves the rules
    // installed (they clear on browser restart) — log it, don't fail the
    // bypass.
    try {
      await dnrPort.rememberBypassTab(tab.id, ruleIds)
    } catch (err) {
      console.error('[docu-kiosk] bypass state persist failed:', err)
    }

    await chrome.tabs.update(tab.id, { url, active: true })
  } catch (err) {
    // A failed bypass would otherwise leave a blank orphan tab behind.
    void chrome.tabs.remove(tab.id)
    console.error('[docu-kiosk] bypass failed:', err)
    throw err
  }
}

async function removeBypassRules(tabId: number) {
  try {
    // forgetBypassTab removes the rules first, then the mapping — a failure
    // leaves the mapping intact and is logged here rather than surfacing as
    // an unhandled rejection.
    await dnrPort.forgetBypassTab(tabId)
  } catch (err) {
    console.error('[docu-kiosk] bypass cleanup failed:', err)
  }
}

export async function installRules() {
  try {
    // Sweep all intercept rules (IDs 1-4) from any prior service-worker run
    // that left them behind, then install the current set.  Bypass rules are
    // session-scoped and auto-clear on browser restart, so no sweep needed.
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    await dnrPort.installInterceptRules(rules, ALL_RULE_IDS)
  } catch (err) {
    // A rejected install (e.g. an oversized regex) used to fail silently and
    // disable interception with no trace — surface it in the SW console.
    console.error('[docu-kiosk] failed to install interception rules:', err)
  }
}

/**
 * Wire the service-worker listeners. This is the only function in the module
 * with side effects — importing the module itself touches nothing (the DNR
 * port is constructed above, but its factory wraps every chrome.* call in a
 * closure), so tests and other embedders can import it without a Chrome
 * runtime. Called exactly once by the service-worker entry
 * (background-main.ts).
 */
export function registerBackgroundListeners(chromeApi: typeof chrome): void {
  // Clicking the toolbar icon opens the settings page in a full tab.
  chromeApi.action.onClicked.addListener(() => {
    void chrome.tabs.create({ url: chrome.runtime.getURL('src/options/index.html') })
  })

  // Debug aid: log every main-frame navigation to a DocuSign host so the
  // original URL is recoverable even when a navigation slips past the
  // interception rule. See it in the service worker console
  // (chrome://extensions → "service worker" → inspect).
  chromeApi.webNavigation.onBeforeNavigate.addListener(
    (details) => {
      if (details.frameId !== 0) return
      console.log('[docu-kiosk] docusign navigation:', details.url)
    },
    { url: [{ hostSuffix: 'docusign.net' }, { hostSuffix: 'docusign.com' }] }
  )

  // Bypass: when a bypass tab closes, remove its allow rules. The mapping
  // comes from storage.session, so cleanup works even when the worker
  // restarted since the bypass was created.
  chromeApi.tabs.onRemoved.addListener((tabId) => {
    void removeBypassRules(tabId)
  })

  // Bypass: listen for bypass requests from the intercepted page.
  chromeApi.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (message?.type === 'bypass' && typeof message.url === 'string') {
      handleBypass(message.url).then(
        () => sendResponse(),
        (err: unknown) => sendResponse({ error: String(err) })
      )
      return true // keep the channel open for the async response
    }
  })
}

