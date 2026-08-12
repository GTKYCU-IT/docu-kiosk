// Broker-client: the intercepted page's single deep module for the broker's
// REST API and the bypass message protocol. The page component keeps only
// view state; every wire contract — URL shapes, message types, retry policy —
// lives here so background.ts and the intercepted page cannot drift apart.
// Like web/src/lib/broker.ts, all runtime dependencies (fetch, the message
// sender) are injected seams so the module is testable without any chrome
// APIs.

declare global {
  // The project's lib target is ES2020, which predates
  // Promise.withResolvers (ES2024). Chrome has shipped it since 119, so
  // declare the member this module uses instead of widening the whole
  // project's lib.
  interface PromiseConstructor {
    withResolvers<T>(): {
      promise: Promise<T>
      resolve: (value: T | PromiseLike<T>) => void
      reject: (reason?: unknown) => void
    }
  }
}

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
// Both calls below strip trailing slashes the same way (and reason) as
// config.ts.

/**
 * List the kiosks currently registered with the broker.
 *
 * Throws on a non-OK response or a body that is not a JSON array; the caller
 * maps the error to a status message.
 */
export async function listKiosks(
  brokerUrl: string,
  fetcher: typeof fetch = fetch
): Promise<Kiosk[]> {
  const res = await fetcher(`${brokerUrl.replace(/\/+$/, '')}/api/kiosks`)
  if (!res.ok) throw new Error(`kiosk listing failed with status ${res.status}`)
  const body: unknown = await res.json() // throws on a non-JSON body
  if (!Array.isArray(body)) throw new Error('kiosk listing returned a non-array body')
  return body as Kiosk[]
}

/**
 * Push a signing URL to a kiosk so the member can sign it.
 *
 * Throws on a non-OK response; the caller maps the error to a status message
 * naming the kiosk.
 */
export async function pushSession(
  brokerUrl: string,
  kioskId: string,
  url: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(
    `${brokerUrl.replace(/\/+$/, '')}/api/kiosks/${kioskId}/sessions`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    }
  )
  if (!res.ok) throw new Error(`session push failed with status ${res.status}`)
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
        const { promise, resolve } = Promise.withResolvers<void>()
        setTimeout(resolve, BYPASS_RETRY_DELAY_MS * (attempt + 1))
        await promise
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
