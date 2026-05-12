import { getConfig } from './config'

getConfig().then(config => {
  chrome.webRequest.onBeforeRequest.addListener((details) => {
    const url = captureSigningUrl(details)

    chrome.storage.session.set({ pendingSigningUrl: url })
    chrome.windows.create({
      url: chrome.runtime.getURL('src/popup/index.html'),
      type: 'popup',
      width: 400,
      height: 280,
      focused: true,
    })

    if (config.kioskUrl) {
      console.log('Intercepted signing URL:', url)
    }

    return { cancel: true }
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
