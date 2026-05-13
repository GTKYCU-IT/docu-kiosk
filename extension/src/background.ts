import { getConfig } from './config'

if (typeof globalThis.chrome !== 'undefined') {
  getConfig().then(() => {
    chrome.webRequest.onBeforeRequest.addListener((details) => {
      const url = captureSigningUrl(details)

      chrome.storage.session.set({ pendingSigningUrl: url })

      return { redirectUrl: chrome.runtime.getURL('src/intercepted/index.html') }
    }, {
      urls: [
        '*://*.docusign.net/*',
        '*://*.docusign.com/*',
      ]
    }, ['requestBody', 'blocking'])
  })
}

export function captureSigningUrl(details: chrome.webRequest.OnBeforeRequestDetails): string {
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
