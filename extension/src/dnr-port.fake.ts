import { BYPASS_RULE_START_ID } from './dnr-port'
import type { DnrPort } from './dnr-port'

export interface FakeDnrPort extends DnrPort {
  dynamicRules: Map<number, chrome.declarativeNetRequest.Rule>
  sessionRules: Map<number, chrome.declarativeNetRequest.Rule>
  /** public so tests can simulate counter drift back to BYPASS_RULE_START_ID */
  nextBypassId: number
  /** public so tests can assert the tab→rule mapping is pruned on close */
  tabRuleIds: Map<number, number[]>
}

export function createFakeDnrPort(): FakeDnrPort {
  const dynamicRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  const sessionRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  const tabRuleIds = new Map<number, number[]>()
  let nextBypassId = BYPASS_RULE_START_ID

  // Serializes persisted-state mutations (ID allocation and the tab→rule
  // map) across concurrent bypass requests, mirroring the chrome adapter
  // (dnr-port.ts): the counter read/advance must not interleave.
  let stateChain: Promise<void> = Promise.resolve()

  return {
    dynamicRules,
    sessionRules,
    tabRuleIds,
    get nextBypassId() {
      return nextBypassId
    },
    set nextBypassId(value: number) {
      nextBypassId = value
    },

    async installInterceptRules(
      addRules: chrome.declarativeNetRequest.Rule[],
      removeRuleIds: number[]
    ): Promise<void> {
      // Mirror chrome: adding a rule whose id is still installed (not covered
      // by the sweep) is rejected. A prior worker run can leave dynamic rules
      // behind, so the startup sweep is observable in tests — without this,
      // dropping the sweep would silently pass.
      const sweptIds = new Set(removeRuleIds)
      for (const rule of addRules) {
        if (!sweptIds.has(rule.id) && dynamicRules.has(rule.id)) {
          throw new Error(`Rule with id ${rule.id} does not have a unique ID`)
        }
      }
      for (const id of removeRuleIds) dynamicRules.delete(id)
      for (const rule of addRules) dynamicRules.set(rule.id, rule)
    },

    async addBypassRules(
      addRules: chrome.declarativeNetRequest.Rule[],
      removeRuleIds: number[]
    ): Promise<void> {
      // Mirror chrome: adding a rule whose id is still installed (not covered
      // by the sweep) is rejected — the same unique-ID constraint as the
      // dynamic ruleset. The counter-drift sweep in handleBypass removes the
      // allocated ids before adding, so a regression that drops the sweep
      // fails the tests instead of silently overwriting.
      const sweptIds = new Set(removeRuleIds)
      for (const rule of addRules) {
        if (!sweptIds.has(rule.id) && sessionRules.has(rule.id)) {
          throw new Error(`Rule with id ${rule.id} does not have a unique ID`)
        }
      }
      for (const id of removeRuleIds) sessionRules.delete(id)
      for (const rule of addRules) sessionRules.set(rule.id, rule)
    },

    async allocateBypassRuleIds(count: number): Promise<number[]> {
      const result = stateChain.then(() => {
        const ids = Array.from({ length: count }, (_, i) => nextBypassId + i)
        nextBypassId += count
        return ids
      })
      stateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    },

    async rememberBypassTab(tabId: number, ruleIds: number[]): Promise<void> {
      const result = stateChain.then(() => {
        tabRuleIds.set(tabId, ruleIds)
      })
      stateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    },

    async forgetBypassTab(tabId: number): Promise<number[] | undefined> {
      const result = stateChain.then(() => {
        const ruleIds = tabRuleIds.get(tabId)
        if (ruleIds === undefined) return undefined
        // Mirrors the chrome adapter: rules are removed before the mapping is
        // forgotten.
        for (const id of ruleIds) sessionRules.delete(id)
        tabRuleIds.delete(tabId)
        return ruleIds
      })
      stateChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    }
  }
}
