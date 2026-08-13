import { vi, type Mock } from 'vitest'
import { BYPASS_RULE_START_ID, createBypass, type Bypass, type BypassDeps } from './bypass'

type DnrRule = chrome.declarativeNetRequest.Rule
type DnrUpdate = { removeRuleIds: number[]; addRules?: DnrRule[] }
type TabOptions = { url: string; active: boolean }

type DnrUpdateFn = (update: DnrUpdate) => Promise<void>
type SessionGetFn = (key: string) => Promise<unknown>
type SessionSetFn = (items: Record<string, unknown>) => Promise<void>
type TabCreateFn = (options: TabOptions) => Promise<{ id?: number }>
type TabUpdateFn = (tabId: number, options: TabOptions) => Promise<unknown>
type TabRemoveFn = (tabId: number) => Promise<unknown>

// Storage keys the real module (bypass.ts) persists under. The harness's own
// store mirrors storage.session, so restart/drift simulation writes the same
// keys the module reads. Duplicated here deliberately: a key change in the
// module makes the harness's state assertions fail loudly instead of silently
// reading stale state.
const BYPASS_ID_STORAGE_KEY = 'bypassNextRuleId'
const BYPASS_TAB_MAP_STORAGE_KEY = 'bypassTabRuleIds'

/**
 * In-memory harness for the REAL bypass module. It supplies fake browser
 * primitives — DNR rulesets, storage.session, tabs — and lets tests drive
 * and inspect the module's own lifecycle (ID allocation, sweep-then-add,
 * persistence, cleanup) through {@link Bypass.open}/{@link Bypass.close}/
 * {@link Bypass.installIntercept}. It does not re-implement any of that
 * orchestration.
 */
export interface FakeBypass extends Bypass {
  /** Dynamic-scope rules currently installed (the four intercept rules). */
  dynamicRules: Map<number, DnrRule>
  /** Session-scope rules currently installed (per-tab bypass allow rules). */
  sessionRules: Map<number, DnrRule>
  /** The persisted tab→rule mapping as the real module last wrote it. */
  tabRuleIds: Map<number, number[]>
  /** Primitive fakes the real module runs against: assert calls, inject failures. */
  dnr: {
    updateDynamicRules: Mock<DnrUpdateFn>
    updateSessionRules: Mock<DnrUpdateFn>
  }
  storage: {
    get: Mock<SessionGetFn>
    set: Mock<SessionSetFn>
  }
  tabs: {
    create: Mock<TabCreateFn>
    update: Mock<TabUpdateFn>
    remove: Mock<TabRemoveFn>
  }
  /** A fresh module instance over the same persisted state — a service-worker restart. */
  newBypass(): Bypass
  /** Drift the persisted ID counter back to BYPASS_RULE_START_ID (simulates a cleared session counter). */
  driftCounter(): void
}

export function createFakeBypass(): FakeBypass {
  const dynamicRules = new Map<number, DnrRule>()
  const sessionRules = new Map<number, DnrRule>()
  const store = new Map<string, unknown>()

  const updateDynamicRules = vi.fn<DnrUpdateFn>(async ({ removeRuleIds, addRules = [] }) => {
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
  })

  const updateSessionRules = vi.fn<DnrUpdateFn>(async ({ removeRuleIds, addRules = [] }) => {
    // Mirror chrome: adding a rule whose id is still installed (not covered
    // by the sweep) is rejected — the same unique-ID constraint as the
    // dynamic ruleset. The counter-drift sweep in open removes the allocated
    // ids before adding, so a regression that drops the sweep fails the tests
    // instead of silently overwriting.
    const sweptIds = new Set(removeRuleIds)
    for (const rule of addRules) {
      if (!sweptIds.has(rule.id) && sessionRules.has(rule.id)) {
        throw new Error(`Rule with id ${rule.id} does not have a unique ID`)
      }
    }
    for (const id of removeRuleIds) sessionRules.delete(id)
    for (const rule of addRules) sessionRules.set(rule.id, rule)
  })

  const sessionGet = vi.fn<SessionGetFn>(async (key) => store.get(key))
  const sessionSet = vi.fn<SessionSetFn>(async (items) => {
    for (const [key, value] of Object.entries(items)) store.set(key, value)
  })

  const tabsCreate = vi.fn<TabCreateFn>(async () => ({ id: 99 }))
  const tabsUpdate = vi.fn<TabUpdateFn>(async () => undefined)
  const tabsRemove = vi.fn<TabRemoveFn>(async () => undefined)

  const deps: BypassDeps = {
    updateDynamicRules,
    updateSessionRules,
    sessionGet,
    sessionSet,
    createTab: tabsCreate,
    updateTab: tabsUpdate,
    removeTab: tabsRemove,
    getInterceptPageUrl: () => 'chrome-extension://testid/src/intercepted/index.html',
  }

  const bypass = createBypass(deps)

  return {
    ...bypass,
    dynamicRules,
    sessionRules,
    get tabRuleIds() {
      const raw = store.get(BYPASS_TAB_MAP_STORAGE_KEY)
      const map = new Map<number, number[]>()
      if (typeof raw === 'object' && raw !== null) {
        for (const [key, value] of Object.entries(raw as Record<string, number[]>)) {
          map.set(Number(key), value)
        }
      }
      return map
    },
    dnr: { updateDynamicRules, updateSessionRules },
    storage: { get: sessionGet, set: sessionSet },
    tabs: { create: tabsCreate, update: tabsUpdate, remove: tabsRemove },
    newBypass: () => createBypass(deps),
    driftCounter: () => store.set(BYPASS_ID_STORAGE_KEY, BYPASS_RULE_START_ID),
  }
}
