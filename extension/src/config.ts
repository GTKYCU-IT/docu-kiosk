export type Config = {
  brokerUrl: string
  kioskUrl: string
}

// Broker URLs are joined with '/api/...' paths, so a trailing slash would
// produce '//api/...'. Go's ServeMux redirects unclean paths, and a 301
// makes browsers rewrite POST to GET — silently breaking send-to-kiosk.
export async function getConfig(): Promise<Config> {
  const [managed, local] = await Promise.all([
    chrome.storage.managed.get(['brokerUrl', 'kioskUrl']).catch(() => ({})),
    chrome.storage.local.get(['brokerUrl', 'kioskUrl'])
  ]) as [Partial<Config>, Partial<Config>]

  return {
    brokerUrl: (managed.brokerUrl ?? local.brokerUrl ?? '').replace(/\/+$/, ''),
    kioskUrl: (managed.kioskUrl ?? local.kioskUrl ?? '').replace(/\/+$/, '')
  }
}
