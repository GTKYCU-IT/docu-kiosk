import { BYPASS_RULE_START_ID } from './dnr-port'
import type { DnrPort } from './dnr-port'

export interface FakeDnrPort extends DnrPort {
  dynamicRules: Map<number, chrome.declarativeNetRequest.Rule>
  sessionRules: Map<number, chrome.declarativeNetRequest.Rule>
  /** public so tests can simulate counter drift back to BYPASS_RULE_START_ID */
  nextBypassId: number
}

export function createFakeDnrPort(): FakeDnrPort {
  const dynamicRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  const sessionRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  let nextBypassId = BYPASS_RULE_START_ID

  // Serializes ID allocation across concurrent bypass requests, mirroring the
  // chrome adapter (dnr-port.ts): the counter read/advance must not interleave.
  let allocationChain: Promise<void> = Promise.resolve()

  return {
    dynamicRules,
    sessionRules,
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
      for (const id of removeRuleIds) sessionRules.delete(id)
      for (const rule of addRules) sessionRules.set(rule.id, rule)
    },

    async removeBypassRules(ruleIds: number[]): Promise<void> {
      for (const id of ruleIds) sessionRules.delete(id)
    },

    async allocateBypassRuleIds(count: number): Promise<number[]> {
      const result = allocationChain.then(() => {
        const ids = Array.from({ length: count }, (_, i) => nextBypassId + i)
        nextBypassId += count
        return ids
      })
      allocationChain = result.then(
        () => undefined,
        () => undefined
      )
      return result
    }
  }
}
