import { getConfig } from './config'

const bypassUrls = new Set<string>()

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg.type === 'openLocally' && typeof msg.url === 'string') {
    bypassUrls.add(msg.url)
    sendResponse({ ok: true })
  }
  return true
})

getConfig().then(() => {
  chrome.webRequest.onBeforeRequest.addListener((details) => {
    const url = captureSigningUrl(details)

    if (bypassUrls.has(url)) {
      bypassUrls.delete(url)
      return {}
    }

    chrome.storage.session.set({ pendingSigningUrl: url }).then(() => {
      chrome.windows.create({
        url: chrome.runtime.getURL('src/popup/index.html'),
        type: 'popup',
        width: 400,
        height: 280,
        focused: true,
      })
    })

    return { redirectUrl: chrome.runtime.getURL('src/intercepted/index.html') }
  }, {
    urls: [
      '*://*.docusign.net/*',
      '*://*.docusign.com/*',
    ]
  }, ['requestBody', 'blocking'])
})

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
