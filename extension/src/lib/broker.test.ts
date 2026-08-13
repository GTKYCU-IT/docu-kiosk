import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  INTERCEPT_HASH_PREFIX,
  BYPASS_MESSAGE_TYPE,
  requestBypass,
  BypassResponseError,
  BypassWakeError,
  BrokerClient,
  type BrokerState,
  type BrokerClientOptions,
} from './broker'

// Minimal Response-like fakes: only the members the module reads. All runtime
// dependencies (fetch, timers) are injected or faked — no chrome APIs are
// touched anywhere in this file.
function okResponse(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function errorResponse(status: number): Response {
  return { ok: false, status } as unknown as Response
}

function makeClient(
  fetcher: typeof fetch,
  onChange: (state: BrokerState) => void = vi.fn(),
  options: Partial<BrokerClientOptions> = {}
): { client: BrokerClient; onChange: (state: BrokerState) => void } {
  const client = new BrokerClient({ url: 'https://broker.local', onChange, fetcher, ...options })
  return { client, onChange }
}

// A listing fetch that never settles unless its AbortSignal is aborted, at
// which point it rejects like a real fetch. Every signal it receives is
// recorded so tests can observe the abort lifecycle. Push POSTs (/sessions)
// resolve normally so push tests can drive the full flow.
function hangingListingFetcher(): { fetcher: typeof fetch; signals: AbortSignal[] } {
  const signals: AbortSignal[] = []
  const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).includes('/sessions')) return Promise.resolve(okResponse(null))
    const signal = init?.signal
    // No abort seam: model the unbounded request — it never settles, exactly
    // like the production hang the timeout contract fixes.
    if (!signal) return new Promise<Response>(() => {})
    signals.push(signal)
    return new Promise<Response>((_resolve, reject) => {
      signal.addEventListener('abort', () =>
        reject(new DOMException('The operation was aborted.', 'AbortError'))
      )
    })
  })
  return { fetcher, signals }
}

describe('wire contract constants', () => {
  it('pins the intercept hash prefix', () => {
    expect(INTERCEPT_HASH_PREFIX).toBe('#url=')
  })

  it('pins the bypass message type', () => {
    expect(BYPASS_MESSAGE_TYPE).toBe('bypass')
  })
})

describe('BrokerClient', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('publishes the loading state at construction and lists kiosks immediately on listKiosks()', async () => {
    const fetcher = vi.fn(async () =>
      okResponse([
        { id: 'k1', name: 'Lobby' },
        { id: 'k2', name: 'Reception' },
      ])
    )
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    expect(onChange).toHaveBeenCalledWith({ status: 'loading', kiosks: [] })

    client.listKiosks()
    // The immediate request fires without waiting for the poll interval.
    expect(fetcher).toHaveBeenCalledWith(
      'https://broker.local/api/kiosks',
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenLastCalledWith({
      status: 'ready',
      kiosks: [
        { id: 'k1', name: 'Lobby' },
        { id: 'k2', name: 'Reception' },
      ],
    })
  })

  it('strips trailing slashes before joining the /api path and reports ready-empty', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    const onChange = vi.fn()
    const client = new BrokerClient({ url: 'https://broker.local///', onChange, fetcher })
    client.listKiosks()
    expect(fetcher).toHaveBeenCalledWith(
      'https://broker.local/api/kiosks',
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenLastCalledWith({ status: 'ready', kiosks: [] })
  })

  it('polls again after the default 3000ms interval', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetcher).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('polls at a custom interval', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    const onChange = vi.fn()
    const client = new BrokerClient({ url: 'https://broker.local', onChange, fetcher, pollIntervalMs: 100 })
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(99)
    expect(fetcher).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('listKiosks() is idempotent: a second call does not start a second loop', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    const { client } = makeClient(fetcher, vi.fn())
    client.listKiosks()
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(4) // one immediate + one poll per interval, not two loops
  })

  it('does not re-publish an unchanged listing on later polls', async () => {
    const kiosks = [{ id: 'k1', name: 'Lobby' }]
    const fetcher = vi.fn(async () => okResponse(kiosks))
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(3000)
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).toHaveBeenCalledTimes(3)
    // loading + first ready only; the two unchanged polls were suppressed.
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('publishes when the listing changes between polls', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(okResponse([{ id: 'k1', name: 'Lobby' }]))
      .mockResolvedValueOnce(
        okResponse([
          { id: 'k1', name: 'Lobby' },
          { id: 'k2', name: 'Reception' },
        ])
      )
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(3000)
    expect(onChange).toHaveBeenLastCalledWith({
      status: 'ready',
      kiosks: [
        { id: 'k1', name: 'Lobby' },
        { id: 'k2', name: 'Reception' },
      ],
    })
  })

  it('reports unreachable on a network failure and recovers on the next poll', async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce(okResponse([{ id: 'k1', name: 'Lobby' }]))
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenLastCalledWith({ status: 'unreachable', kiosks: [] })
    // The loop keeps polling and reports the recovered broker.
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(onChange).toHaveBeenLastCalledWith({
      status: 'ready',
      kiosks: [{ id: 'k1', name: 'Lobby' }],
    })
  })

  it('reports unreachable on an error status and on a non-array body', async () => {
    for (const response of [errorResponse(503), okResponse({ error: 'nope' })]) {
      const fetcher = vi.fn(async () => response)
      const onChange = vi.fn()
      const { client } = makeClient(fetcher, onChange)
      client.listKiosks()
      await vi.advanceTimersByTimeAsync(0)
      expect(onChange).toHaveBeenLastCalledWith({ status: 'unreachable', kiosks: [] })
      client.close()
    }
  })

  it('aborts a listing hung past the poll deadline, reports unreachable, and starts a later poll', async () => {
    const { fetcher, signals } = hangingListingFetcher()
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()

    // The listing request carries an AbortSignal, still live while pending.
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(signals[0]).toBeInstanceOf(AbortSignal)
    expect(signals[0].aborted).toBe(false)
    expect(onChange).toHaveBeenCalledTimes(1) // loading only — a hang is not yet a failure

    // A request hung past the per-listing deadline is aborted at
    // pollIntervalMs, not before.
    await vi.advanceTimersByTimeAsync(2999)
    expect(signals[0].aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(signals[0].aborted).toBe(true)
    // The timeout is a failure: unreachable is published, and no immediate
    // retry replaces the normal poll schedule.
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenLastCalledWith({ status: 'unreachable', kiosks: [] })

    // After the normal poll delay a fresh listing request starts.
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(signals[1]).toBeInstanceOf(AbortSignal)
    expect(signals[1].aborted).toBe(false)
    client.close()
  })

  it('uses pollIntervalMs as the per-listing deadline', async () => {
    const { fetcher, signals } = hangingListingFetcher()
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange, { pollIntervalMs: 100 })
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(99)
    expect(signals[0].aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(signals[0].aborted).toBe(true)
    expect(onChange).toHaveBeenLastCalledWith({ status: 'unreachable', kiosks: [] })
    // The recovery poll follows one interval later.
    await vi.advanceTimersByTimeAsync(100)
    expect(fetcher).toHaveBeenCalledTimes(2)
    client.close()
  })

  it('POSTs {url} to the /sessions wire endpoint on push', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/sessions')) return okResponse(null)
      return okResponse([])
    })
    const { client } = makeClient(fetcher, vi.fn())
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)

    await client.push('k1', 'https://sign.example/abc')
    expect(fetcher).toHaveBeenCalledWith('https://broker.local/api/kiosks/k1/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: 'https://sign.example/abc' }),
    })
  })

  it('pauses polling while a push is in flight and leaves it paused after success', async () => {
    let releasePush: (res: Response) => void = () => {}
    const fetcher = vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('/sessions')) {
        return new Promise<Response>((resolve) => {
          releasePush = resolve
        })
      }
      return Promise.resolve(okResponse([{ id: 'k1', name: 'Lobby' }]))
    })
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetcher).toHaveBeenCalledTimes(1)

    const pushing = client.push('k1', 'https://sign.example/abc')
    // No polls fire while the push is in flight.
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(2) // listing + push POST
    releasePush(okResponse(null))
    await pushing
    // Success leaves polling paused — the page closes.
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('aborts the active listing when a push starts without publishing unreachable', async () => {
    const { fetcher, signals } = hangingListingFetcher()
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledTimes(1) // loading only

    await client.push('k1', 'https://sign.example/abc')
    // The push supersedes the in-flight listing: it is aborted, and the
    // abort is not a timeout, so no unreachable state is published.
    expect(signals[0].aborted).toBe(true)
    expect(onChange).toHaveBeenCalledTimes(1)

    // Polling stays paused after the successful push — no resumed polls and
    // no stale deadline can fire afterwards.
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(2) // listing + push POST
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('resumes polling after a failed push and throws a push-named error', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/sessions')) return errorResponse(500)
      return okResponse([])
    })
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetcher).toHaveBeenCalledTimes(1)

    await expect(client.push('k1', 'https://sign.example/abc')).rejects.toThrow(
      'push failed with status 500'
    )
    // Polling automatically resumes: the next poll fires after the interval.
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).toHaveBeenCalledTimes(3) // listing + push + resumed poll
  })

  it('close cancels the recurring poll', async () => {
    const fetcher = vi.fn(async () => okResponse([]))
    const { client } = makeClient(fetcher, vi.fn())
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    client.close()
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('does not publish from an in-flight listing that settles after close', async () => {
    let releaseListing: (res: Response) => void = () => {}
    const fetcher = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          releaseListing = resolve
        })
    )
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    expect(onChange).toHaveBeenCalledTimes(1) // loading only
    client.close()
    releaseListing(okResponse([{ id: 'k1', name: 'Lobby' }]))
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('aborts the active listing on close without publishing unreachable', async () => {
    const { fetcher, signals } = hangingListingFetcher()
    const onChange = vi.fn()
    const { client } = makeClient(fetcher, onChange)
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenCalledTimes(1) // loading only

    client.close()
    expect(signals[0].aborted).toBe(true)
    // The abort is not a timeout: no unreachable state is published and no
    // further poll fires.
    await vi.advanceTimersByTimeAsync(0)
    expect(onChange).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('does not resume polling when a push fails after close', async () => {
    let releasePush: (res: Response) => void = () => {}
    const fetcher = vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('/sessions')) {
        return new Promise<Response>((resolve) => {
          releasePush = resolve
        })
      }
      return Promise.resolve(okResponse([]))
    })
    const { client } = makeClient(fetcher, vi.fn())
    client.listKiosks()
    await vi.advanceTimersByTimeAsync(0)
    const pushing = client.push('k1', 'https://sign.example/abc').catch((err) => err)
    client.close()
    releasePush(errorResponse(500))
    await pushing
    await vi.advanceTimersByTimeAsync(9000)
    expect(fetcher).toHaveBeenCalledTimes(2) // listing + push POST; no resumed polls
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
    // Attach the rejection handler before advancing timers: the final throw
    // lands while advanceTimersByTimeAsync drains the microtask queue, so a
    // handler attached afterwards would be flagged as an unhandled rejection.
    const rejected = expect(pending).rejects.toBeInstanceOf(BypassWakeError)
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(300)
    await vi.advanceTimersByTimeAsync(600)
    await rejected
    expect(sender).toHaveBeenCalledTimes(3)
  })
})
