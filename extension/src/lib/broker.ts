// Broker: the intercepted page's single deep module for the broker's REST
// API, the kiosk-list lifecycle, and the bypass message protocol. The page
// component keeps only view state; every wire contract — URL shapes, message
// types, polling policy — lives here so background.ts and the intercepted
// page cannot drift apart.
// Like web/src/lib/broker.ts, all runtime dependencies (fetch, the message
// sender) are injected seams so the module is testable without any chrome
// APIs.

/**
 * Hash fragment the interception redirect appends to the original signing
 * URL (`${interceptBaseUrl}#url=<original>`). The intercepted page strips
 * this prefix to recover the signing link. Shared with background.ts so the
 * two ends of the redirect can never disagree on the format.
 */
export const INTERCEPT_HASH_PREFIX = '#url='

/**
 * Message type the intercepted page sends to wake the service worker and
 * request a bypass. The background onMessage listener matches on it; the
 * intercepted page sends `{ type: BYPASS_MESSAGE_TYPE, url }`.
 */
export const BYPASS_MESSAGE_TYPE = 'bypass'

/** The broker's /api/kiosks entry shape. */
export type Kiosk = { id: string; name: string }

// MV3 service workers sleep when idle, and the intercepted page may have been
// open long past the worker's lifetime. chrome.runtime.sendMessage wakes the
// worker, and the first attempt can race with startup — retry a bounded
// number of times with linear backoff, giving the worker a fresh wake-up per
// attempt. This is the exact policy the intercepted page inlined before this
// module existed (3 attempts, 300ms·attempt backoff).
const BYPASS_MAX_ATTEMPTS = 3
const BYPASS_RETRY_DELAY_MS = 300

/**
 * The service worker ran and rejected the bypass itself (its response carried
 * an `error` field). Not retried: the failure is in the bypass flow, not in
 * the worker wake-up. `message` is the worker's reason.
 */
export class BypassResponseError extends Error {
  constructor(reason: string) {
    super(reason)
    this.name = 'BypassResponseError'
  }
}

/**
 * Every wake attempt failed — sendMessage itself rejected on all attempts, so
 * the service worker never answered. The caller surfaces its own
 * copy-to-clipboard fallback for this case.
 */
export class BypassWakeError extends Error {
  constructor() {
    super('all sendMessage attempts failed')
    this.name = 'BypassWakeError'
  }
}

type Sender = (msg: unknown) => Promise<unknown>

// chrome.runtime.sendMessage must be invoked with its receiver intact, so the
// default wraps it instead of passing the method reference around. MV3 returns
// a promise when no callback is supplied.
const defaultSender: Sender = (msg) => chrome.runtime.sendMessage(msg)

// Broker URLs are joined with '/api/...' paths, so a trailing slash would
// produce '//api/...'. Go's ServeMux redirects unclean paths, and a 301
// makes browsers rewrite POST to GET — silently breaking send-to-kiosk.
// Same reason (and approach) as config.ts.
function apiUrl(base: string): string {
  return base.replace(/\/+$/, '')
}

/**
 * Lifecycle of the broker-facing side of the intercepted page.
 *
 * Statuses the page can render:
 * - `loading`: no listing has completed yet (initial state, published at
 *   construction).
 * - `ready`: the latest listing succeeded; `kiosks.length === 0` renders the
 *   empty state, otherwise the kiosk list.
 * - `unreachable`: the latest listing failed (network error, non-OK status,
 *   malformed body, or a request aborted by its own deadline); `kiosks`
 *   keeps the last successful listing. Polling continues so a recovered
 *   broker is reported again.
 */
export type BrokerStatus = 'loading' | 'ready' | 'unreachable'

/** A snapshot delivered to `onChange`; every state transition flows through it. */
export interface BrokerState {
  status: BrokerStatus
  kiosks: Kiosk[]
}

export interface BrokerClientOptions {
  url: string
  onChange: (state: BrokerState) => void
  /** fetch seam; defaults to the global fetch. */
  fetcher?: typeof fetch
  /** delay between polls, in ms. Defaults to 3000. */
  pollIntervalMs?: number
}

// The timer handle type differs between runtimes (DOM libs return `number`,
// @types/node returns `Timeout`), so capture it in a closure and expose only a
// named `clear()` operation — same approach as web/src/lib/broker.ts.
interface PollTimer {
  clear(): void
}

/**
 * Owns the kiosk listing loop and the send-to-kiosk push so the page
 * component holds no wire logic:
 *
 * - `listKiosks()` starts an immediate listing request and a recurring poll
 *   (default 3000ms). Identical listings are not re-published, so unchanged
 *   polls never cause redundant updates. A listing that does not settle
 *   within one poll interval is aborted and reported unreachable, so a hung
 *   request can never stall the loop.
 * - `push(kioskId, url)` POSTs to the wire endpoint, pauses polling while
 *   the push is in flight, and automatically resumes polling if it fails. A
 *   successful push leaves polling paused — the page closes.
 * - `close()` tears the client down: no timers fire and no in-flight work
 *   publishes afterwards.
 */
export class BrokerClient {
  private readonly url: string
  private readonly onChange: (state: BrokerState) => void
  private readonly fetcher: typeof fetch
  private readonly pollIntervalMs: number
  private timer: PollTimer | null = null
  // The in-flight listing's abort handle and its deadline timer. Only the
  // listing request runs under these; push and close reuse the handle to
  // cancel a listing, and the deadline is what turns a never-settling
  // listing into a timed-out poll instead of a stalled loop.
  private controller: AbortController | null = null
  private deadline: PollTimer | null = null
  private started = false
  private inFlight = false
  private paused = false
  private closed = false
  private status: BrokerStatus = 'loading'
  private kiosks: Kiosk[] = []

  constructor(options: BrokerClientOptions) {
    this.url = options.url
    this.onChange = options.onChange
    this.fetcher = options.fetcher ?? fetch
    this.pollIntervalMs = options.pollIntervalMs ?? 3000
    // The view starts in the loading state; every later transition is
    // delivered through onChange.
    this.onChange({ status: 'loading', kiosks: [] })
  }

  /**
   * Start the listing loop: one immediate request, then a recurring poll.
   * Idempotent — a second call while the loop is running does nothing.
   */
  listKiosks(): void {
    if (this.closed || this.started) return
    this.started = true
    void this.pollNow()
  }

  /**
   * Push a signing URL to a kiosk so the member can sign it. Pauses polling
   * for the duration of the push; a failed push resumes it and rethrows so
   * the caller can surface its own status. A successful push leaves polling
   * paused — the page closes.
   */
  async push(kioskId: string, url: string): Promise<void> {
    if (this.closed) throw new Error('broker client is closed')
    this.paused = true
    this.timer?.clear()
    this.timer = null
    // Abort a listing still in flight so it cannot settle mid-push. The
    // abort is a cancellation, not a broker failure — pollNow checks
    // `paused` before publishing, so nothing is emitted for it.
    this.controller?.abort()
    try {
      const res = await this.fetcher(`${apiUrl(this.url)}/api/kiosks/${kioskId}/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url }),
      })
      if (!res.ok) throw new Error(`push failed with status ${res.status}`)
    } catch (err) {
      if (this.closed) throw err
      this.paused = false
      if (this.started) this.schedulePoll()
      throw err
    }
  }

  /** Teardown: no timers fire and no further callbacks after this. */
  close(): void {
    if (this.closed) return
    this.closed = true
    this.timer?.clear()
    this.timer = null
    this.deadline?.clear()
    this.deadline = null
    // Cancel a listing still in flight; pollNow checks `closed` before
    // publishing anything, so the abort emits no state.
    this.controller?.abort()
  }

  private async pollNow(): Promise<void> {
    if (this.closed || this.inFlight || this.paused) return
    this.inFlight = true
    // Every listing runs under a deadline: after one poll interval the
    // request is aborted and the poll reports unreachable, so a listing
    // that never settles cannot stall the loop — the scheduler simply
    // starts the next poll as usual. push() and close() abort the same
    // handle to cancel the listing without publishing anything.
    const controller = new AbortController()
    this.controller = controller
    const handle = setTimeout(() => controller.abort(), this.pollIntervalMs)
    this.deadline = { clear: () => clearTimeout(handle) }
    try {
      const res = await this.fetcher(`${apiUrl(this.url)}/api/kiosks`, {
        signal: controller.signal,
      })
      if (this.closed) return
      if (!res.ok) throw new Error(`kiosk listing failed with status ${res.status}`)
      const body: unknown = await res.json() // throws on a non-JSON body
      if (this.closed) return
      if (!Array.isArray(body)) throw new Error('kiosk listing returned a non-array body')
      this.publish(body as Kiosk[], 'ready')
    } catch {
      // Network errors, non-OK statuses, malformed bodies, and deadline
      // aborts all mean the broker is unreachable right now; keep the last
      // listing and let the recurring poll report recovery. An abort from
      // push() or close() publishes nothing: push sets `paused` and close
      // sets `closed` before aborting, and both are checked here.
      if (this.closed || this.paused) return
      this.publish(this.kiosks, 'unreachable')
    } finally {
      this.deadline?.clear()
      this.deadline = null
      // Only clear the handle this poll created: a stale poll's finally
      // must never clear a newer listing's controller.
      if (this.controller === controller) this.controller = null
      this.inFlight = false
      if (!this.closed && !this.paused) this.schedulePoll()
    }
  }

  private schedulePoll(): void {
    if (this.closed || this.paused || this.timer !== null) return
    const handle = setTimeout(() => {
      this.timer = null
      void this.pollNow()
    }, this.pollIntervalMs)
    this.timer = { clear: () => clearTimeout(handle) }
  }

  private publish(kiosks: Kiosk[], status: BrokerStatus): void {
    if (this.closed) return
    // Idempotent polling: an unchanged listing is not a state change, so it
    // is suppressed instead of re-published every poll.
    const unchanged =
      this.status === status &&
      kiosks.length === this.kiosks.length &&
      kiosks.every((kiosk, i) => kiosk.id === this.kiosks[i].id && kiosk.name === this.kiosks[i].name)
    if (unchanged) return
    this.status = status
    this.kiosks = kiosks
    this.onChange({ status, kiosks })
  }
}

/**
 * Ask the service worker to open `url` in a real browser tab, bypassing
 * interception.
 *
 * Sends `{ type: BYPASS_MESSAGE_TYPE, url }` through `sender` (default
 * chrome.runtime.sendMessage), retrying with 300ms·attempt backoff when the
 * send itself fails — the worker may be sleeping and need waking. A response
 * carrying `error` throws {@link BypassResponseError} immediately (no retry);
 * exhausting all attempts throws {@link BypassWakeError}. Resolves once the
 * worker acknowledges the bypass.
 */
export async function requestBypass(url: string, sender: Sender = defaultSender): Promise<void> {
  for (let attempt = 0; attempt < BYPASS_MAX_ATTEMPTS; attempt++) {
    let response: unknown
    try {
      response = await sender({ type: BYPASS_MESSAGE_TYPE, url })
    } catch (err) {
      // sendMessage itself failed — the SW may not be running yet
      console.warn('[docu-kiosk] sendMessage attempt %d failed: %o', attempt + 1, err)
      if (attempt < BYPASS_MAX_ATTEMPTS - 1) {
        // Back off before the next wake attempt, giving the worker time to boot.
        await new Promise<void>((resolve) => setTimeout(resolve, BYPASS_RETRY_DELAY_MS * (attempt + 1)))
      }
      continue
    }
    if (response && typeof response === 'object' && 'error' in response) {
      // The SW ran and rejected the bypass itself — surface the reason
      // immediately instead of retrying.
      throw new BypassResponseError(String(response.error))
    }
    return // success — the tab will open
  }
  // All retries exhausted
  throw new BypassWakeError()
}
