import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  INTERCEPT_HASH_PREFIX,
  BYPASS_MESSAGE_TYPE,
  listKiosks,
  pushSession,
  requestBypass,
  BypassResponseError,
  BypassWakeError,
} from './broker-client'

// Minimal Response-like fakes: only the members the module reads. No chrome
// APIs are touched anywhere in this file.
function okResponse(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function errorResponse(status: number): Response {
  return { ok: false, status } as unknown as Response
}

describe('wire contract constants', () => {
  it('pins the intercept hash prefix', () => {
    expect(INTERCEPT_HASH_PREFIX).toBe('#url=')
  })

  it('pins the bypass message type', () => {
    expect(BYPASS_MESSAGE_TYPE).toBe('bypass')
  })
})

describe('listKiosks', () => {
  it('GETs /api/kiosks and parses the kiosk list', async () => {
    const fetcher = vi.fn(async () =>
      okResponse([
        { id: 'k1', name: 'Lobby' },
        { id: 'k2', name: 'Reception' },
      ])
    )
    await expect(listKiosks('https://broker.local', fetcher)).resolves.toEqual([
      { id: 'k1', name: 'Lobby' },
      { id: 'k2', name: 'Reception' },
    ])
    expect(fetcher).toHaveBeenCalledWith('https://broker.local/api/kiosks')
  })

  it('strips trailing slashes before joining the /api path', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    await listKiosks('https://broker.local///', fetcher)
    expect(fetcher).toHaveBeenCalledWith('https://broker.local/api/kiosks')
  })

  it('throws when the broker responds with an error status', async () => {
    const fetcher = vi.fn(async () => errorResponse(503))
    await expect(listKiosks('https://broker.local', fetcher)).rejects.toThrow()
  })

  it('throws when the body is not a JSON array', async () => {
    const fetcher = vi.fn(async () => okResponse({ error: 'nope' }))
    await expect(listKiosks('https://broker.local', fetcher)).rejects.toThrow(
      'non-array body'
    )
  })
})

describe('pushSession', () => {
  it('POSTs {url} to /api/kiosks/:id/sessions', async () => {
    const fetcher = vi.fn(async () => okResponse(null))
    await pushSession('https://broker.local', 'k1', 'https://sign.example/abc', fetcher)
    expect(fetcher).toHaveBeenCalledWith('https://broker.local/api/kiosks/k1/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: 'https://sign.example/abc' }),
    })
  })

  it('throws when the broker responds with an error status', async () => {
    const fetcher = vi.fn(async () => errorResponse(500))
    await expect(pushSession('https://broker.local', 'k1', 'u', fetcher)).rejects.toThrow()
  })
})

describe('requestBypass', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('sends the bypass message and resolves on the first attempt', async () => {
    const sender = vi.fn(async () => undefined)
    await requestBypass('https://sign.example/abc', sender)
    expect(sender).toHaveBeenCalledTimes(1)
    expect(sender).toHaveBeenCalledWith({
      type: BYPASS_MESSAGE_TYPE,
      url: 'https://sign.example/abc',
    })
  })

  it('retries with backoff while the worker wakes, then succeeds', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const sender = vi
      .fn()
      .mockRejectedValueOnce(new Error('Receiving end does not exist'))
      .mockRejectedValueOnce(new Error('Receiving end does not exist'))
      .mockResolvedValueOnce(undefined)
    const pending = requestBypass('https://sign.example/abc', sender)
    // Let the first rejected send settle so its retry timer is registered,
    // then advance through the 300ms and 600ms backoffs.
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(300)
    await vi.advanceTimersByTimeAsync(600)
    await expect(pending).resolves.toBeUndefined()
    expect(sender).toHaveBeenCalledTimes(3)
  })

  it('throws immediately when the worker responds with an error (no retry)', async () => {
    const sender = vi.fn(async () => ({ error: 'quota exhausted' }))
    const pending = requestBypass('https://sign.example/abc', sender)
    await expect(pending).rejects.toBeInstanceOf(BypassResponseError)
    await expect(pending).rejects.toThrow('quota exhausted')
    expect(sender).toHaveBeenCalledTimes(1)
  })

  it('throws after all wake attempts are exhausted', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const sender = vi.fn(async () => {
      throw new Error('Receiving end does not exist')
    })
    const pending = requestBypass('https://sign.example/abc', sender)
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(300)
    await vi.advanceTimersByTimeAsync(600)
    await expect(pending).rejects.toBeInstanceOf(BypassWakeError)
    expect(sender).toHaveBeenCalledTimes(3)
  })
})
