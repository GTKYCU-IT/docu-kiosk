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

  it('removes bypass rules via updateSessionRules without addRules', async () => {
    await createChromeDnrPort().removeBypassRules([100, 101])
    expect(updateSessionRules).toHaveBeenCalledWith({ removeRuleIds: [100, 101] })
    expect(updateSessionRules.mock.calls[0][0]).not.toHaveProperty('addRules')
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

  it('removes bypass rules from the session map', async () => {
    const port = createFakeDnrPort()
    await port.addBypassRules([makeRule(100), makeRule(101)], [])
    await port.removeBypassRules([100])
    expect(port.sessionRules.has(100)).toBe(false)
    expect(port.sessionRules.has(101)).toBe(true)
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
})
