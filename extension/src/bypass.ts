import { INTERCEPT_HASH_PREFIX } from './lib/broker-client'

/** Rule IDs 1–4 are the intercept patterns. IDs 5–99 are reserved. IDs 100+ are per-tab bypass allow rules. */
export const BYPASS_RULE_START_ID = 100

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

/**
 * The tab→rule map (which allow-rule IDs belong to which bypass tab) also
 * lives in chrome.storage.session, for the same reason as the counter: the
 * service worker restarts while the session rules survive, so tab-close
 * cleanup cannot rely on module memory. It is cleared together with the
 * session rules on browser restart.
 */
const BYPASS_TAB_MAP_STORAGE_KEY = 'bypassTabRuleIds'

type DnrRule = chrome.declarativeNetRequest.Rule

/**
 * Internal seam: the browser primitives the bypass lifecycle needs. The
 * production adapter wires these to the chrome.* APIs; the test harness
 * (bypass.fake.ts) supplies in-memory fakes. This is an implementation seam —
 * background.ts only ever sees the {@link Bypass} interface.
 */
export interface BypassDeps {
  /** Sweep-then-add dynamic-scope rules (the intercept rules). */
  updateDynamicRules(update: { removeRuleIds: number[]; addRules?: DnrRule[] }): Promise<void>
  /** Sweep-then-add session-scope rules (per-tab bypass allow rules). */
  updateSessionRules(update: { removeRuleIds: number[]; addRules?: DnrRule[] }): Promise<void>
  /** Read one storage.session key; resolves to undefined when absent. */
  sessionGet(key: string): Promise<unknown>
  /** Write storage.session items. */
  sessionSet(items: Record<string, unknown>): Promise<void>
  /** Create a new tab; resolves with its id (undefined when Chrome refused one). */
  createTab(options: { url: string; active: boolean }): Promise<{ id?: number }>
  /** Navigate an existing tab. */
  updateTab(tabId: number, options: { url: string; active: boolean }): Promise<unknown>
  /** Close a tab. */
  removeTab(tabId: number): Promise<unknown>
  /** Absolute URL of the intercepted-page entry the redirect rules point at. */
  getInterceptPageUrl(): string
}

/**
 * The bypass module's external seam: everything the service worker needs to
 * open bypass tabs, clean them up, and keep interception installed. No
 * declarative-net-request details cross this seam — no rules, no IDs, no
 * storage keys.
 */
export interface Bypass {
  /**
   * Open a DocuSign URL in a new browser tab, bypassing interception.
   * Resolves with the tab id once the navigation has started.
   */
  open(url: string): Promise<number>
  /**
   * Remove a bypass tab's allow rules and forget its mapping. Safe to call
   * for unknown tab ids; failures are logged, never surfaced.
   */
  close(tabId: number): Promise<void>
  /**
   * Sweep-then-install the interception rules (dynamic scope). Failures are
   * logged, never surfaced.
   */
  installIntercept(): Promise<void>
}

/**
 * The bypass lifecycle, implemented once against injected browser
 * primitives. `createChromeBypass()` wires it to the real chrome.* APIs;
 * bypass.fake.ts wires it to in-memory fakes so tests exercise the real
 * lifecycle — allocation, sweep-then-add, persistence, cleanup — through
 * {@link Bypass.open}/{@link Bypass.close}/{@link Bypass.installIntercept}.
 */
export function createBypass(deps: BypassDeps): Bypass {
  // Serializes persisted-session-state mutations — ID allocation and the
  // tab→rule map — across concurrent bypass requests within one worker lifetime
  // (storage.session has no compare-and-swap primitive).
  let stateChain: Promise<void> = Promise.resolve()

  function chain<T>(task: () => Promise<T>): Promise<T> {
    const result = stateChain.then(task)
    stateChain = result.then(
      () => undefined,
      () => undefined
    )
    return result
  }

  // Reads the persisted tab→rule map. storage.session stores JSON-serializable
  // values only, hence a plain Record rather than a Map — object keys come back
  // as strings, so entries are validated via Number(key) round-trip. A stored
  // value of the wrong shape falls back to an empty map rather than failing
  // cleanup (entry-level corruption is dropped too).
  async function readBypassTabMap(): Promise<Record<number, number[]>> {
    const raw: unknown = await deps.sessionGet(BYPASS_TAB_MAP_STORAGE_KEY)
    if (typeof raw !== 'object' || raw === null) return {}
    const map: Record<number, number[]> = {}
    for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
      const tabId = Number(key)
      if (
        Number.isInteger(tabId) &&
        Array.isArray(value) &&
        value.every((v) => Number.isInteger(v))
      ) {
        map[tabId] = value
      }
    }
    return map
  }

  /** Allocate `count` consecutive bypass rule IDs, persisted across service-worker restarts. */
  function allocateBypassRuleIds(count: number): Promise<number[]> {
    return chain(async () => {
      const raw = await deps.sessionGet(BYPASS_ID_STORAGE_KEY)
      const next = typeof raw === 'number' && Number.isInteger(raw) ? raw : BYPASS_RULE_START_ID
      await deps.sessionSet({ [BYPASS_ID_STORAGE_KEY]: next + count })
      return Array.from({ length: count }, (_, i) => next + i)
    })
  }

  /** Persist the tab→rule mapping so tab-close cleanup survives worker restarts. */
  function rememberBypassTab(tabId: number, ruleIds: number[]): Promise<void> {
    return chain(async () => {
      const map = await readBypassTabMap()
      map[tabId] = ruleIds
      await deps.sessionSet({ [BYPASS_TAB_MAP_STORAGE_KEY]: map })
    })
  }

  /**
   * Remove a tab's bypass rules and forget its mapping; returns the removed
   * rule IDs, or undefined when unmapped.
   */
  function forgetBypassTab(tabId: number): Promise<number[] | undefined> {
    return chain(async () => {
      const map = await readBypassTabMap()
      const ruleIds = map[tabId]
      if (ruleIds === undefined) return undefined
      // Remove the rules BEFORE forgetting the mapping: a failed removal
      // leaves the mapping intact so the rules keep an owner (a stale entry
      // is overwritten if the tabId is ever reused) instead of orphaning
      // them for the rest of the browser session.
      await deps.updateSessionRules({ removeRuleIds: ruleIds })
      delete map[tabId]
      await deps.sessionSet({ [BYPASS_TAB_MAP_STORAGE_KEY]: map })
      return ruleIds
    })
  }

  function buildRules(interceptBaseUrl: string): DnrRule[] {
    const action: chrome.declarativeNetRequest.RuleAction = {
      type: 'redirect',
      redirect: { regexSubstitution: `${interceptBaseUrl}${INTERCEPT_HASH_PREFIX}\\0` }
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

  function buildBypassRules(tabId: number, startId: number): DnrRule[] {
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

  return {
    /**
     * Open a DocuSign URL in a new browser tab, bypassing interception.
     *
     * Creates a blank tab to obtain a stable tabId, installs tab-scoped allow
     * rules that out-prioritise the intercept redirect rules, then navigates
     * the tab to the URL. Every step is awaited so the service worker stays
     * alive until the rules are committed and the navigation has started.
     *
     * The allow rules protect the tab for its entire lifetime — including
     * multi-page signing flows and subsequent same-tab navigation.
     */
    async open(url: string): Promise<number> {
      const tab = await deps.createTab({ url: 'about:blank', active: false })
      if (!tab.id) throw new Error('tab has no id')

      try {
        const ruleIds = await allocateBypassRuleIds(2)
        const rules = buildBypassRules(tab.id, ruleIds[0])
        // Sweep-then-add in one atomic call: if the counter ever drifts from the
        // installed rules (e.g. the session counter is cleared on extension
        // reload while session rules persisted), stale rules with these IDs are
        // removed first instead of rejecting the whole update with "Rule with id
        // N does not have a unique ID". Live rules never carry these IDs — the
        // counter is monotonic within a browser session — so this cannot disable
        // another tab's bypass.
        await deps.updateSessionRules({ removeRuleIds: ruleIds, addRules: rules })
        // Persist the tab→rule mapping with the same lifetime as the session
        // rules: the worker restarts long before a bypass tab closes, so cleanup
        // cannot rely on module memory. A persistence failure leaves the rules
        // installed (they clear on browser restart) — log it, don't fail the
        // bypass.
        try {
          await rememberBypassTab(tab.id, ruleIds)
        } catch (err) {
          console.error('[docu-kiosk] bypass state persist failed:', err)
        }

        await deps.updateTab(tab.id, { url, active: true })
        return tab.id
      } catch (err) {
        // A failed bypass would otherwise leave a blank orphan tab behind.
        void deps.removeTab(tab.id)
        console.error('[docu-kiosk] bypass failed:', err)
        throw err
      }
    },

    /**
     * Remove a bypass tab's allow rules and forget its mapping.
     *
     * forgetBypassTab removes the rules first, then the mapping — a failure
     * leaves the mapping intact (the rules keep an owner, so a later close
     * retries the removal) and is logged here rather than surfacing as an
     * unhandled rejection in the tab-removed listener.
     */
    async close(tabId: number): Promise<void> {
      try {
        await forgetBypassTab(tabId)
      } catch (err) {
        console.error('[docu-kiosk] bypass cleanup failed:', err)
      }
    },

    /**
     * Sweep-then-install the interception rules (dynamic scope).
     *
     * Sweeps all intercept rules (IDs 1-4) from any prior service-worker run
     * that left them behind, then installs the current set. Bypass rules are
     * session-scoped and auto-clear on browser restart, so no sweep needed.
     * A rejected install (e.g. an oversized regex) used to fail silently and
     * disable interception with no trace — log it in the SW console instead.
     */
    async installIntercept(): Promise<void> {
      try {
        const rules = buildRules(deps.getInterceptPageUrl())
        await deps.updateDynamicRules({ removeRuleIds: ALL_RULE_IDS, addRules: rules })
      } catch (err) {
        console.error('[docu-kiosk] failed to install interception rules:', err)
      }
    }
  }
}

/**
 * Production adapter: the chrome-backed bypass. All chrome.* access is
 * deferred inside the method calls — importing this module or constructing
 * the bypass never touches the Chrome runtime.
 */
export function createChromeBypass(): Bypass {
  return createBypass({
    updateDynamicRules: (update) => chrome.declarativeNetRequest.updateDynamicRules(update),
    updateSessionRules: (update) => chrome.declarativeNetRequest.updateSessionRules(update),
    sessionGet: async (key) => (await chrome.storage.session.get(key))[key],
    sessionSet: (items) => chrome.storage.session.set(items),
    createTab: (options) => chrome.tabs.create(options),
    updateTab: (tabId, options) => chrome.tabs.update(tabId, options),
    removeTab: (tabId) => chrome.tabs.remove(tabId),
    getInterceptPageUrl: () => chrome.runtime.getURL('src/intercepted/index.html'),
  })
}
