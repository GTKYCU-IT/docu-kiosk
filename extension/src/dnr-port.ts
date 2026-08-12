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

// Serializes ID allocation across concurrent bypass requests within one
// worker lifetime (storage.session has no compare-and-swap primitive).
let bypassAllocationChain: Promise<void> = Promise.resolve()

export interface DnrPort {
  /** Sweep-then-add interception rules (dynamic scope). */
  installInterceptRules(addRules: chrome.declarativeNetRequest.Rule[], removeRuleIds: number[]): Promise<void>
  /** Sweep-then-add bypass allow rules (session scope). */
  addBypassRules(addRules: chrome.declarativeNetRequest.Rule[], removeRuleIds: number[]): Promise<void>
  /** Remove bypass allow rules (session scope). */
  removeBypassRules(ruleIds: number[]): Promise<void>
  /** Allocate `count` consecutive bypass rule IDs, persisted across service-worker restarts. */
  allocateBypassRuleIds(count: number): Promise<number[]>
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
      const result = bypassAllocationChain.then(async () => {
        const stored = await chrome.storage.session.get(BYPASS_ID_STORAGE_KEY)
        const raw = stored[BYPASS_ID_STORAGE_KEY]
        const next = typeof raw === 'number' && Number.isInteger(raw) ? raw : BYPASS_RULE_START_ID
        await chrome.storage.session.set({ [BYPASS_ID_STORAGE_KEY]: next + count })
        return Array.from({ length: count }, (_, i) => next + i)
      })
      bypassAllocationChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    }
  }
}
