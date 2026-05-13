export type Config = {
  brokerUrl: string
  kioskUrl: string
}

export async function getConfig(): Promise<Config> {
  const [managed, local] = await Promise.all([
    chrome.storage.managed.get(['brokerUrl', 'kioskUrl']).catch(() => ({})),
    chrome.storage.local.get(['brokerUrl', 'kioskUrl'])
  ]) as [Partial<Config>, Partial<Config>]

  return {
    brokerUrl: managed.brokerUrl ?? local.brokerUrl ?? '',
    kioskUrl: managed.kioskUrl ?? local.kioskUrl ?? ''
  }
}
