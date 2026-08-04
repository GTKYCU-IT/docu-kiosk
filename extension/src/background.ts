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

export async function installRules() {
  try {
    const rules = buildRules(chrome.runtime.getURL('src/intercepted/index.html'))
    await chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds: ALL_RULE_IDS, addRules: rules })
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
}
