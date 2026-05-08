export type Config = {
  kioskId: string
  brokerUrl: string
  kioskUrl: string
}

export async function getConfig(): Promise<Config> {
  const [managed, local] = await Promise.all([
    chrome.storage.managed.get(['kioskId', 'brokerUrl', 'kioskUrl']),
    chrome.storage.local.get(['kioskId', 'brokerUrl', 'kioskUrl'])
  ]) as [Partial<Config>, Partial<Config>]

  return {
    kioskId: managed.kioskId ?? local.kioskId ?? 'unknown-kiosk',
    brokerUrl: managed.brokerUrl ?? local.brokerUrl ?? '',
    kioskUrl: managed.kioskUrl ?? local.kioskUrl ?? ''
  }
}
