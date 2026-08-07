import { BYPASS_RULE_START_ID } from './dnr-port'
import type { DnrPort } from './dnr-port'

export interface FakeDnrPort extends DnrPort {
  dynamicRules: Map<number, chrome.declarativeNetRequest.Rule>
  sessionRules: Map<number, chrome.declarativeNetRequest.Rule>
  /** public so tests can simulate counter drift back to 100 */
  nextBypassId: number
}

export function createFakeDnrPort(): FakeDnrPort {
  const dynamicRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  const sessionRules = new Map<number, chrome.declarativeNetRequest.Rule>()
  let nextBypassId = BYPASS_RULE_START_ID

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
      const ids = Array.from({ length: count }, (_, i) => nextBypassId + i)
      nextBypassId += count
      return ids
    }
  }
}
