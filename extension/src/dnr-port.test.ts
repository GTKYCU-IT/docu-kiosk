import { describe, it, expect, vi, beforeEach, afterAll } from 'vitest'
import { createChromeDnrPort, BYPASS_RULE_START_ID } from './dnr-port'
import { createFakeDnrPort } from './dnr-port.fake'

// Minimal chrome stub: only the declarative-net-request and storage.session
// APIs the adapter touches. storage.session is backed by a stateful Map, so
// values survive adapter recreation ("service-worker restart" = create a new
// adapter over the same map; "browser restart" = clear the map).
const sessionStore = new Map<string, unknown>()
const updateDynamicRules = vi.fn()
const updateSessionRules = vi.fn()
const storageSessionGet = vi.fn(async (key: string) => {
  const value = sessionStore.get(key)
  return value === undefined ? {} : { [key]: value }
})
const storageSessionSet = vi.fn(async (items: Record<string, unknown>) => {
  for (const [key, value] of Object.entries(items)) sessionStore.set(key, value)
})

vi.stubGlobal('chrome', {
  declarativeNetRequest: {
    updateDynamicRules,
    updateSessionRules,
  },
  storage: {
    session: { get: storageSessionGet, set: storageSessionSet }
  }
})

function makeRule(id: number): chrome.declarativeNetRequest.Rule {
  return {
    id,
    priority: 1,
    action: { type: 'allow' },
    condition: {}
  }
}

beforeEach(() => {
  sessionStore.clear()
  vi.clearAllMocks()
  updateDynamicRules.mockResolvedValue(undefined)
  updateSessionRules.mockResolvedValue(undefined)
})

afterAll(() => vi.unstubAllGlobals())

describe('createChromeDnrPort', () => {
  it('installs interception rules via updateDynamicRules', async () => {
    const rules = [makeRule(1), makeRule(2)]
    await createChromeDnrPort().installInterceptRules(rules, [1, 2])
    expect(updateDynamicRules).toHaveBeenCalledWith({ removeRuleIds: [1, 2], addRules: rules })
  })

  it('adds bypass rules via updateSessionRules', async () => {
    const rules = [makeRule(100), makeRule(101)]
    await createChromeDnrPort().addBypassRules(rules, [100, 101])
    expect(updateSessionRules).toHaveBeenCalledWith({ removeRuleIds: [100, 101], addRules: rules })
  })

  it('removes a remembered tab\'s rules via updateSessionRules without addRules', async () => {
    const port = createChromeDnrPort()
    await port.rememberBypassTab(7, [100, 101])
    await port.forgetBypassTab(7)
    expect(updateSessionRules).toHaveBeenCalledWith({ removeRuleIds: [100, 101] })
    expect(updateSessionRules.mock.calls[0][0]).not.toHaveProperty('addRules')
  })

  it('keeps the mapping intact when the rule removal fails', async () => {
    const port = createChromeDnrPort()
    await port.rememberBypassTab(7, [100, 101])
    const error = new Error('session rules rejected')
    updateSessionRules.mockRejectedValueOnce(error)
    await expect(port.forgetBypassTab(7)).rejects.toBe(error)
    // The rules still have an owner: a retry can clean them up.
    await expect(port.forgetBypassTab(7)).resolves.toEqual([100, 101])
  })

  it('serializes concurrent forgets without resurrecting entries', async () => {
    const port = createChromeDnrPort()
    await port.rememberBypassTab(7, [100, 101])
    await port.rememberBypassTab(8, [102, 103])
    const [first, second] = await Promise.all([
      port.forgetBypassTab(7),
      port.forgetBypassTab(8),
    ])
    expect(first).toEqual([100, 101])
    expect(second).toEqual([102, 103])
    // Without chain serialization, one forget would write back a map that
    // re-includes the other tab's entry.
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({})
  })

  it('propagates updateDynamicRules rejections', async () => {
    const error = new Error('dynamic rules rejected')
    updateDynamicRules.mockRejectedValueOnce(error)
    await expect(createChromeDnrPort().installInterceptRules([], [])).rejects.toBe(error)
  })

  it('allocates consecutive IDs from the persisted counter', async () => {
    const port = createChromeDnrPort()
    await expect(port.allocateBypassRuleIds(2)).resolves.toEqual([100, 101])
    await expect(port.allocateBypassRuleIds(2)).resolves.toEqual([102, 103])
  })

  it('continues the sequence on a new adapter over the same session store (worker restart)', async () => {
    const first = createChromeDnrPort()
    await first.allocateBypassRuleIds(2)
    await first.allocateBypassRuleIds(2)
    await expect(createChromeDnrPort().allocateBypassRuleIds(2)).resolves.toEqual([104, 105])
  })

  it('restarts at 100 after a cleared store and a new adapter (browser restart)', async () => {
    const first = createChromeDnrPort()
    await first.allocateBypassRuleIds(2)
    sessionStore.clear()
    await expect(createChromeDnrPort().allocateBypassRuleIds(2)).resolves.toEqual([100, 101])
  })

  it('falls back to 100 when the stored counter is corrupted', async () => {
    sessionStore.set('bypassNextRuleId', 'not a number')
    await expect(createChromeDnrPort().allocateBypassRuleIds(2)).resolves.toEqual([100, 101])
  })

  it('serializes concurrent allocations without overlapping IDs', async () => {
    const port = createChromeDnrPort()
    const [first, second] = await Promise.all([
      port.allocateBypassRuleIds(2),
      port.allocateBypassRuleIds(2)
    ])
    const all = [...first, ...second].sort((a, b) => a - b)
    expect(all).toEqual([100, 101, 102, 103])
  })

  it('remembers the tab→rule mapping in storage.session', async () => {
    await createChromeDnrPort().rememberBypassTab(7, [100, 101])
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({ 7: [100, 101] })
  })

  it('forgets a tab mapping and returns its rule IDs', async () => {
    const port = createChromeDnrPort()
    await port.rememberBypassTab(7, [100, 101])
    await expect(port.forgetBypassTab(7)).resolves.toEqual([100, 101])
    expect(sessionStore.get('bypassTabRuleIds')).toEqual({})
  })

  it('forget returns undefined for an unmapped tab', async () => {
    await expect(createChromeDnrPort().forgetBypassTab(7)).resolves.toBeUndefined()
  })

  it('keeps other tabs mapped when forgetting one tab', async () => {
    const port = createChromeDnrPort()
    await port.rememberBypassTab(7, [100, 101])
    await port.rememberBypassTab(8, [102, 103])
    await port.forgetBypassTab(7)
    await expect(port.forgetBypassTab(8)).resolves.toEqual([102, 103])
  })

  it('keeps the tab→rule mapping across adapter recreation (worker restart)', async () => {
    const first = createChromeDnrPort()
    await first.rememberBypassTab(7, [100, 101])
    await expect(createChromeDnrPort().forgetBypassTab(7)).resolves.toEqual([100, 101])
  })

  it('clears the tab→rule mapping with the store (browser restart)', async () => {
    const first = createChromeDnrPort()
    await first.rememberBypassTab(7, [100, 101])
    sessionStore.clear()
    await expect(createChromeDnrPort().forgetBypassTab(7)).resolves.toBeUndefined()
  })

  it('tolerates a corrupted stored tab map', async () => {
    sessionStore.set('bypassTabRuleIds', 'garbage')
    const port = createChromeDnrPort()
    await expect(port.forgetBypassTab(7)).resolves.toBeUndefined()
    await port.rememberBypassTab(7, [100, 101])
    await expect(port.forgetBypassTab(7)).resolves.toEqual([100, 101])
  })
})

describe('createFakeDnrPort', () => {
  it('sweeps then installs interception rules into the dynamic map', async () => {
    const port = createFakeDnrPort()
    port.dynamicRules.set(1, makeRule(1))
    const fresh = makeRule(1)
    await port.installInterceptRules([fresh, makeRule(2)], [1, 99])
    expect(port.dynamicRules.get(1)).toBe(fresh)
    expect(port.dynamicRules.get(2)).toBeDefined()
    expect(port.dynamicRules.has(99)).toBe(false)
    expect(port.sessionRules.size).toBe(0)
  })

  it('sweeps then adds bypass rules into the session map', async () => {
    const port = createFakeDnrPort()
    port.sessionRules.set(100, makeRule(100))
    const fresh = makeRule(100)
    await port.addBypassRules([fresh, makeRule(101)], [100, 199])
    expect(port.sessionRules.get(100)).toBe(fresh)
    expect(port.sessionRules.get(101)).toBeDefined()
    expect(port.sessionRules.has(199)).toBe(false)
    expect(port.dynamicRules.size).toBe(0)
  })

  it('allocates consecutive IDs and advances the counter', async () => {
    const port = createFakeDnrPort()
    await expect(port.allocateBypassRuleIds(2)).resolves.toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1
    ])
    await expect(port.allocateBypassRuleIds(2)).resolves.toEqual([
      BYPASS_RULE_START_ID + 2,
      BYPASS_RULE_START_ID + 3
    ])
    expect(port.nextBypassId).toBe(BYPASS_RULE_START_ID + 4)
  })

  it('supports simulating counter drift back to the start', async () => {
    const port = createFakeDnrPort()
    await port.allocateBypassRuleIds(2)
    port.nextBypassId = BYPASS_RULE_START_ID
    await expect(port.allocateBypassRuleIds(2)).resolves.toEqual([
      BYPASS_RULE_START_ID,
      BYPASS_RULE_START_ID + 1
    ])
  })

  it('never overlaps across repeated allocations', async () => {
    const port = createFakeDnrPort()
    const first = await port.allocateBypassRuleIds(2)
    const second = await port.allocateBypassRuleIds(2)
    const all = [...first, ...second]
    expect(new Set(all).size).toBe(all.length)
  })

  it('remembers and forgets the tab→rule mapping, removing the rules too', async () => {
    const port = createFakeDnrPort()
    await port.addBypassRules([makeRule(100), makeRule(101)], [])
    await port.rememberBypassTab(7, [100, 101])
    expect(port.tabRuleIds.get(7)).toEqual([100, 101])
    await expect(port.forgetBypassTab(7)).resolves.toEqual([100, 101])
    expect(port.tabRuleIds.size).toBe(0)
    expect(port.sessionRules.size).toBe(0)
    await expect(port.forgetBypassTab(7)).resolves.toBeUndefined()
  })
})
