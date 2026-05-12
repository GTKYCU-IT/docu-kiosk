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

  let sending = false

  async function refreshKiosks() {
    if (sending) return

    let kiosks: Kiosk[]
    try {
      const res = await fetch(`${config.brokerUrl}/api/kiosks`)
      kiosks = await res.json()
    } catch {
      statusEl.textContent = 'Could not reach broker. Check that it is running.'
      return
    }

    if (sending) return

    const prevIds = new Set(
      Array.from(listEl.querySelectorAll<HTMLButtonElement>('button')).map(b => b.dataset.id)
    )
    const unchanged = kiosks.length === prevIds.size && kiosks.every(k => prevIds.has(k.id))
    if (unchanged) return

    listEl.innerHTML = ''

    if (kiosks.length === 0) {
      statusEl.textContent = 'No kiosks are connected. Waiting…'
      return
    }

    statusEl.textContent = 'Select a kiosk:'

    for (const kiosk of kiosks) {
      const btn = document.createElement('button')
      btn.textContent = kiosk.name
      btn.dataset.id = kiosk.id
      btn.addEventListener('click', async () => {
        sending = true
        clearInterval(pollInterval)
        listEl.querySelectorAll('button').forEach(b => ((b as HTMLButtonElement).disabled = true))
        statusEl.textContent = `Sending to ${kiosk.name}…`

        try {
          const res = await fetch(`${config.brokerUrl}/api/kiosks/${kiosk.id}/sessions`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url: pendingUrl }),
          })

          if (!res.ok) throw new Error(`${res.status}`)

          await chrome.storage.session.remove('pendingSigningUrl')
          window.close()
        } catch {
          statusEl.textContent = `Failed to send to ${kiosk.name}.`
          sending = false
          listEl.querySelectorAll('button').forEach(b => ((b as HTMLButtonElement).disabled = false))
          pollInterval = setInterval(refreshKiosks, 3000)
        }
      })
      listEl.appendChild(btn)
    }

  }

  await refreshKiosks()
  let pollInterval = setInterval(refreshKiosks, 3000)
}

main()
