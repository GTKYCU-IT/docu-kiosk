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

/**
 * Session-scoped DNR rules survive service-worker restarts (MV3 workers are
 * killed after ~30s idle), but module state does not — so the next free
 * bypass rule ID lives in chrome.storage.session, which has exactly the same
 * lifetime as the session rules: it survives worker restarts and is cleared
 * on browser restart. Without this, a bypass after a worker restart reused
 * IDs 100/101 while the previous tab's rules were still installed, and Chrome
 * rejected the update ("Rule with id 100 does not have a unique ID").
 */
const BYPASS_ID_STORAGE_KEY = 'bypassNextRuleId'

// Serializes ID allocation across concurrent bypass requests within one
// worker lifetime (storage.session has no compare-and-swap primitive).
let bypassAllocationChain: Promise<void> = Promise.resolve()

async function nextBypassRuleIds(): Promise<number[]> {
  const result = bypassAllocationChain.then(async () => {
    const stored = await chrome.storage.session.get(BYPASS_ID_STORAGE_KEY)
    const raw = stored[BYPASS_ID_STORAGE_KEY]
    const next = typeof raw === 'number' && Number.isInteger(raw) ? raw : BYPASS_RULE_START_ID
    await chrome.storage.session.set({ [BYPASS_ID_STORAGE_KEY]: next + 2 })
    return [next, next + 1]
  })
  bypassAllocationChain = result.then(
    () => undefined,
    () => undefined
  )
  return result
}

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
    const ruleIds = await nextBypassRuleIds()
    const rules = buildBypassRules(tab.id, ruleIds[0])
    await chrome.declarativeNetRequest.updateSessionRules({ addRules: rules })
    bypassRuleIds.set(tab.id, ruleIds)

    await chrome.tabs.update(tab.id, { url, active: true })
  } catch (err) {
    // A failed bypass would otherwise leave a blank orphan tab behind.
    void chrome.tabs.remove(tab.id)
    console.error('[docu-kiosk] bypass failed:', err)
    throw err
  }
}

function removeBypassRules(tabId: number) {
  const ruleIds = bypassRuleIds.get(tabId)
  if (!ruleIds) return
  bypassRuleIds.delete(tabId)
  void chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: ruleIds })
}


export async function installRules() {
  try {
    // Sweep all intercept rules (IDs 1-4) from any prior service-worker run
    // that left them behind, then install the current set.  Bypass rules are
    // session-scoped and auto-clear on browser restart, so no sweep needed.
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    await chrome.declarativeNetRequest.updateDynamicRules({
      removeRuleIds: ALL_RULE_IDS,
      addRules: rules
    })
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

