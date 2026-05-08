export function getConfig() {
  const managed = chrome.storage.managed.get([
    "kioskId",
    "brokerUrl"
  ]) as { kioskId?: string; brokerUrl?: string; };

  const local = chrome.storage.local.get([
    "kioskId",
    "brokerUrl"
  ]) as { kioskId?: string; brokerUrl?: string; };

  return {
    kioskId: managed.kioskId ?? local.kioskId ?? "unknown-kiosk",
    brokerUrl: managed.brokerUrl ?? local.brokerUrl ?? ""
  }
}
