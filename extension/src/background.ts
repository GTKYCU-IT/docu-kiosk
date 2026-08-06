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
/** Rule IDs 1–4 are the intercept patterns. */

const ALL_RULE_IDS = [1, 2, 3, 4]

/** Window (ms) during which DocuSign interception is disabled for a bypass. */
const BYPASS_WINDOW_MS = 15_000

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
/**
 * Open a DocuSign URL in a new browser tab, bypassing interception.
 *
 * The service worker temporarily removes every intercept rule for a short
 * window so the tab — and any redirect chain DocuSign performs — loads without
 * re-interception.  After the window the rules are reinstalled.
 */
export async function handleBypass(url: string) {
  // Remove every intercept rule so the tab can navigate freely.
  await chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds: ALL_RULE_IDS })

  let restored = false
  const restoreRules = () => {
    if (restored) return
    restored = true
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    chrome.declarativeNetRequest.updateDynamicRules({ addRules: rules }).catch(
      (err: unknown) => console.error('[docu-kiosk] bypass: failed to restore intercept rules:', err)
    )
  }

  try {
    const tab = await chrome.tabs.create({ url, active: true })

    // Restore rules when the main frame finishes loading (covers 302
    // redirect chains) — or after the safety window, whichever comes first.
    const listener = (details: chrome.webNavigation.WebNavigationFramedCallbackDetails) => {
      if (details.tabId === tab.id && details.frameId === 0) {
        chrome.webNavigation.onCompleted.removeListener(listener)
        restoreRules()
      }
    }
    chrome.webNavigation.onCompleted.addListener(listener)
    setTimeout(restoreRules, BYPASS_WINDOW_MS)
  } catch (err) {
    // Tab creation failed — restore rules immediately so interception works
    // for other tabs.
    restoreRules()
    throw err
  }
}


export async function installRules() {
  try {
    // Sweep all intercept rules (IDs 1-4) from any prior service-worker run
    // that left them behind, then install the current set.
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    await chrome.declarativeNetRequest.updateDynamicRules({
      removeRuleIds: ALL_RULE_IDS,
      addRules: rules
    })
  } catch (err) {
    // A rejected install (e.g. an oversized regex) used to fail silently
    // and disable interception with no trace — surface it in the SW console.
    console.error('[docu-kiosk] failed to install interception rules:', err)
  }
}

if (typeof globalThis.chrome !== 'undefined') {
  void installRules()

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

  // Listen for bypass requests from the intercepted page.
  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (message?.type === 'bypass' && typeof message.url === 'string') {
      handleBypass(message.url).then(
        () => sendResponse(),
        (err: unknown) => sendResponse({ error: String(err) })
      )
      return true // keep the channel open for the async response
    }
  })
}

