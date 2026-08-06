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
/** Rule IDs 1–4 are the intercept patterns. IDs 5–99 are reserved. IDs 100+ are per-tab bypass allow rules. */
const BYPASS_RULE_START_ID = 100
let nextBypassRuleId = BYPASS_RULE_START_ID

const ALL_RULE_IDS = [1, 2, 3, 4]
const BYPASS_HOSTS = ['*://*.docusign.net/*', '*://*.docusign.com/*']

/** Active bypass allow-rule IDs, keyed by tabId. Used for cleanup when the tab closes. */
const bypassRuleIds = new Map<number, number[]>()

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
 * Two-phase strategy:
 * 1. Remove every intercept rule so the initial navigation (and any 302
 *    redirect chain) loads without re-interception.
 * 2. Once the main frame finishes loading, install a tab-scoped allow rule
 *    and restore the global intercept rules.  The allow rule protects all
 *    subsequent navigations in the same tab — including multi-page signing
 *    flows — for the tab's entire lifetime.
 */
export async function handleBypass(url: string) {
  // Phase 1: remove all intercept rules so the tab can navigate freely.
  await chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds: ALL_RULE_IDS })

  let finished = false
  const finish = (tabId: number) => {
    if (finished) return
    finished = true
    const allowIds = [nextBypassRuleId, nextBypassRuleId + 1]
    const allowRules = buildBypassRules(tabId, nextBypassRuleId)
    nextBypassRuleId += 2
    const interceptRules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    bypassRuleIds.set(tabId, allowIds)
    chrome.declarativeNetRequest.updateDynamicRules({
      addRules: [...allowRules, ...interceptRules]
    }).catch(
      (err: unknown) => console.error('[docu-kiosk] bypass: failed to install rules:', err)
    )
  }

  // Safety: if onCompleted never fires, still finish after 15 s.
  const SAFETY_TIMEOUT_MS = 15_000

  try {
    const tab = await chrome.tabs.create({ url, active: true })
    if (!tab.id) throw new Error('tab has no id')

    const listener = (details: chrome.webNavigation.WebNavigationFramedCallbackDetails) => {
      if (details.tabId === tab.id && details.frameId === 0) {
        chrome.webNavigation.onCompleted.removeListener(listener)
        finish(tab.id!)
      }
    }
    chrome.webNavigation.onCompleted.addListener(listener)
    setTimeout(() => finish(tab.id!), SAFETY_TIMEOUT_MS)
  } catch (err) {
    // Tab creation failed — restore intercept rules immediately.
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    chrome.declarativeNetRequest.updateDynamicRules({ addRules: rules }).catch(() => {})
    throw err
  }
}

function removeBypassRules(tabId: number) {
  const ruleIds = bypassRuleIds.get(tabId)
  if (!ruleIds) return
  bypassRuleIds.delete(tabId)
  void chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds: ruleIds })
}


export async function installRules() {
  try {
    // Sweep all intercept rules (IDs 1-4) and any stale bypass rules (100+)
    // from a prior service-worker run that ended without onRemoved firing.
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    await chrome.declarativeNetRequest.updateDynamicRules({
      removeRuleIds: ALL_RULE_IDS,
      addRules: rules
    })
    // Clean up any stale bypass rules (100+) left over from a previous
    // service-worker instance that terminated without onRemoved firing.
    const existing = await chrome.declarativeNetRequest.getDynamicRules()
    const staleBypassIds = existing
      .map((r) => r.id)
      .filter((id) => id >= BYPASS_RULE_START_ID)
    if (staleBypassIds.length > 0) {
      await chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds: staleBypassIds })
    }
  } catch (err) {
    // A rejected install (e.g. an oversized regex) used to fail silently and
    // disable interception with no trace — surface it in the SW console.
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

  // Bypass: when a bypass tab closes, remove its allow rules.
  chrome.tabs.onRemoved.addListener((tabId) => removeBypassRules(tabId))

  // Bypass: listen for bypass requests from the intercepted page.
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

