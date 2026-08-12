/** Rule IDs 1–4 are the intercept patterns. IDs 5–99 are reserved. IDs 100+ are per-tab bypass allow rules. */
export const BYPASS_RULE_START_ID = 100

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

// Serializes persisted-session-state mutations — ID allocation and the
// tab→rule map — across concurrent bypass requests within one worker lifetime
// (storage.session has no compare-and-swap primitive).
let bypassStateChain: Promise<void> = Promise.resolve()

// Reads the persisted tab→rule map. A stored value of the wrong shape falls
// back to an empty map rather than failing cleanup (entry-level corruption is
// dropped too).
async function readBypassTabMap(): Promise<Record<number, number[]>> {
  const stored = await chrome.storage.session.get(BYPASS_TAB_MAP_STORAGE_KEY)
  const raw: unknown = stored[BYPASS_TAB_MAP_STORAGE_KEY]
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

export interface DnrPort {
  /** Sweep-then-add interception rules (dynamic scope). */
  installInterceptRules(addRules: chrome.declarativeNetRequest.Rule[], removeRuleIds: number[]): Promise<void>
  /** Sweep-then-add bypass allow rules (session scope). */
  addBypassRules(addRules: chrome.declarativeNetRequest.Rule[], removeRuleIds: number[]): Promise<void>
  /** Remove bypass allow rules (session scope). */
  removeBypassRules(ruleIds: number[]): Promise<void>
  /** Allocate `count` consecutive bypass rule IDs, persisted across service-worker restarts. */
  allocateBypassRuleIds(count: number): Promise<number[]>
  /** Persist the tab→rule mapping so tab-close cleanup survives worker restarts. */
  rememberBypassTab(tabId: number, ruleIds: number[]): Promise<void>
  /** Remove and return a tab's mapped rule IDs, or undefined when unmapped. */
  forgetBypassTab(tabId: number): Promise<number[] | undefined>
}

export function createChromeDnrPort(): DnrPort {
  return {
    async installInterceptRules(
      addRules: chrome.declarativeNetRequest.Rule[],
      removeRuleIds: number[]
    ): Promise<void> {
      await chrome.declarativeNetRequest.updateDynamicRules({ removeRuleIds, addRules })
    },

    async addBypassRules(
      addRules: chrome.declarativeNetRequest.Rule[],
      removeRuleIds: number[]
    ): Promise<void> {
      await chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds, addRules })
    },

    async removeBypassRules(ruleIds: number[]): Promise<void> {
      await chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: ruleIds })
    },

    async allocateBypassRuleIds(count: number): Promise<number[]> {
      const result = bypassStateChain.then(async () => {
        const stored = await chrome.storage.session.get(BYPASS_ID_STORAGE_KEY)
        const raw = stored[BYPASS_ID_STORAGE_KEY]
        const next = typeof raw === 'number' && Number.isInteger(raw) ? raw : BYPASS_RULE_START_ID
        await chrome.storage.session.set({ [BYPASS_ID_STORAGE_KEY]: next + count })
        return Array.from({ length: count }, (_, i) => next + i)
      })
      bypassStateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    },

    async rememberBypassTab(tabId: number, ruleIds: number[]): Promise<void> {
      const result = bypassStateChain.then(async () => {
        const map = await readBypassTabMap()
        map[tabId] = ruleIds
        await chrome.storage.session.set({ [BYPASS_TAB_MAP_STORAGE_KEY]: map })
      })
      bypassStateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    },

    async forgetBypassTab(tabId: number): Promise<number[] | undefined> {
      const result = bypassStateChain.then(async () => {
        const map = await readBypassTabMap()
        const ruleIds = map[tabId]
        if (ruleIds === undefined) return undefined
        delete map[tabId]
        await chrome.storage.session.set({ [BYPASS_TAB_MAP_STORAGE_KEY]: map })
        return ruleIds
      })
      bypassStateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    }
  }
}
