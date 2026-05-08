import { getConfig } from './config'
import type { Config } from './config'

let config: Config = { kioskId: 'unknown-kiosk', brokerUrl: '', kioskUrl: '' }
getConfig().then(c => { config = c })

function captureSigningUrl(details: chrome.webRequest.OnBeforeRequestDetails): string {
  let url = details.url

  if (details.method === 'POST' && details.requestBody?.formData) {
    try {
      const params = new URLSearchParams()
      for (const [key, values] of Object.entries(details.requestBody.formData)) {
        if (values && values.length > 0) params.append(key, values[0].toString())
      }
      url = `${url}?${params.toString()}`
    } catch (e) {
      console.error('Could not parse request body', e)
    }
  }

  return url
}

function injectReturnUrl(signingUrl: string, kioskId: string, kioskUrl: string): string {
  try {
    const url = new URL(signingUrl)
    url.searchParams.set('returnUrl', `${kioskUrl}?id=${kioskId}`)
    return url.toString()
  } catch {
    return signingUrl
  }
}

console.log('DocuSign interceptor loaded')
chrome.webRequest.onBeforeRequest.addListener((details) => {
  const raw = captureSigningUrl(details)
  const url = config.kioskUrl ? injectReturnUrl(raw, config.kioskId, config.kioskUrl) : raw

  console.log('Intercepted signing URL:', url)

  if (config.brokerUrl) {
    fetch(config.brokerUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kioskId: config.kioskId, url })
    }).catch(err => console.error('Broker call failed:', err))
  }

  return { cancel: true }
}, {
  urls: [
    '*://*.docusign.net/*',
    '*://*.docusign.com/*',
  ]
}, ['requestBody', 'blocking'])
