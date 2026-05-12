import { getConfig } from '../config'

type Kiosk = { id: string; name: string }

async function main() {
  const statusEl = document.getElementById('status')!
  const listEl = document.getElementById('kiosk-list')!

  const [config, session] = await Promise.all([
    getConfig(),
    chrome.storage.session.get('pendingSigningUrl') as Promise<{ pendingSigningUrl?: string }>,
  ])

  const pendingUrl = session.pendingSigningUrl

  if (!config.brokerUrl) {
    statusEl.textContent = 'Broker URL not configured. Open extension options to set it.'
    return
  }

  if (!pendingUrl) {
    statusEl.textContent = 'No pending signing request.'
    return
  }

  let kiosks: Kiosk[]
  try {
    const res = await fetch(`${config.brokerUrl}/api/kiosks`)
    kiosks = await res.json()
  } catch {
    statusEl.textContent = 'Could not reach broker. Check that it is running.'
    return
  }

  if (kiosks.length === 0) {
    statusEl.textContent = 'No kiosks are connected.'
    return
  }

  statusEl.textContent = 'Select a kiosk:'

  for (const kiosk of kiosks) {
    const btn = document.createElement('button')
    btn.textContent = kiosk.name
    btn.addEventListener('click', async () => {
      btn.disabled = true
      statusEl.textContent = `Sending to ${kiosk.name}…`

      try {
        let urlToSend = pendingUrl
        try {
          const parsed = new URL(pendingUrl)
          parsed.searchParams.set('returnUrl', `${config.brokerUrl}/signed`)
          urlToSend = parsed.toString()
        } catch {
          // use raw URL if parsing fails
        }

        const res = await fetch(`${config.brokerUrl}/api/kiosks/${kiosk.id}/sessions`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ url: urlToSend }),
        })

        if (!res.ok) {
          throw new Error(`${res.status}`)
        }

        await chrome.storage.session.remove('pendingSigningUrl')
        window.close()
      } catch (e) {
        statusEl.textContent = `Failed to send to ${kiosk.name}.`
        btn.disabled = false
      }
    })
    listEl.appendChild(btn)
  }
}

main()
