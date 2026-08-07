import { describe, it, expect, vi, afterAll } from 'vitest'
import { getConfig } from './config'

// getConfig reads only chrome.storage.managed and chrome.storage.local; stub
// just those two APIs, nothing else.
const storageManagedGet = vi.fn()
const storageLocalGet = vi.fn()

vi.stubGlobal('chrome', {
  storage: {
    managed: { get: storageManagedGet },
    local: { get: storageLocalGet }
  }
})

afterAll(() => vi.unstubAllGlobals())

describe('config', () => {
  it('falls back to the local brokerUrl when managed storage is empty', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })

  it('prefers the managed (policy-pushed) brokerUrl', async () => {
    storageManagedGet.mockResolvedValue({ brokerUrl: 'https://broker.managed' })
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.managed')
  })

  it('strips a trailing slash from the local brokerUrl', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local/' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })

  it('strips trailing slashes from the managed brokerUrl and kioskUrl', async () => {
    storageManagedGet.mockResolvedValue({ brokerUrl: 'https://broker.managed///', kioskUrl: 'https://kiosk.local/' })
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.managed')
    expect(cfg.kioskUrl).toBe('https://kiosk.local')
  })

  it('leaves a slash-less brokerUrl unchanged', async () => {
    storageManagedGet.mockResolvedValue({})
    storageLocalGet.mockResolvedValue({ brokerUrl: 'https://broker.local' })
    const cfg = await getConfig()
    expect(cfg.brokerUrl).toBe('https://broker.local')
  })
})
