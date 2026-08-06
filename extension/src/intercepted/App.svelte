<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { getConfig } from '../config'
  import { Button } from '$lib/components/ui/button'
  import { ExternalLink, Send } from '@lucide/svelte'
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card'
  import { toast } from 'svelte-sonner'

  type Kiosk = { id: string; name: string }

  let status = $state('')
  let kiosks = $state<Kiosk[]>([])
  let sending = $state(false)
  let copied = $state(false)
  let brokerUrl = $state('')
  let pendingUrl = $state('')
  let pollInterval: ReturnType<typeof setInterval> | undefined

  onMount(async () => {
    const hash = window.location.hash
    if (!hash.startsWith('#url=')) {
      status = 'No signing link intercepted.'
      return
    }

    pendingUrl = hash.slice('#url='.length)

    const cfg = await getConfig()
    brokerUrl = cfg.brokerUrl ?? ''

    if (!brokerUrl) {
      status = 'Broker URL not configured. Open the extension options to set it.'
      return
    }

    await refreshKiosks()
    pollInterval = setInterval(refreshKiosks, 3000)
  })

  onDestroy(() => clearInterval(pollInterval))

  async function refreshKiosks() {
    if (sending) return
    try {
      const res = await fetch(`${brokerUrl}/api/kiosks`)
      const fetched: Kiosk[] = await res.json()
      const prevIds = new Set(kiosks.map(k => k.id))
      const unchanged = fetched.length === prevIds.size && fetched.every(k => prevIds.has(k.id))
      if (unchanged) return
      kiosks = fetched
      status = fetched.length === 0 ? 'No kiosks are connected. Waiting…' : 'Select a kiosk:'
    } catch {
      status = 'Could not reach broker. Check that it is running.'
    }
  }

  async function sendToKiosk(kiosk: Kiosk) {
    sending = true
    clearInterval(pollInterval)
    status = `Sending to ${kiosk.name}…`
    try {
      const res = await fetch(`${brokerUrl}/api/kiosks/${kiosk.id}/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: pendingUrl }),
      })
      if (!res.ok) throw new Error(`${res.status}`)
      window.close()
    } catch {
      status = `Failed to send to ${kiosk.name}.`
      sending = false
      pollInterval = setInterval(refreshKiosks, 3000)
    }
  }

  async function bypass() {
    const response = await chrome.runtime.sendMessage({ type: 'bypass', url: pendingUrl }).catch(() => ({ error: 'sendMessage failed' }))
    if (response?.error) {
      await copyPendingUrl()
      toast.error('Could not open in browser. The URL has been copied to your clipboard.')
    }
  }

  async function copyPendingUrl() {
    try {
      await navigator.clipboard.writeText(pendingUrl)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = pendingUrl
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    copied = true
    setTimeout(() => { copied = false }, 1500)
  }
</script>

<div class="flex min-h-svh items-center justify-center bg-muted p-4">
  <Card class="w-full max-w-sm">
    <CardHeader>
      <CardTitle>Send to Kiosk</CardTitle>
      <CardDescription>
        The signing link has been intercepted. Select a kiosk below to send the document to the member.
      </CardDescription>
    </CardHeader>
    <CardContent class="flex flex-col gap-2">
      {#if sending}
        <p class="text-sm text-muted-foreground">{status}</p>
      {:else if kiosks.length > 0}
        {#each kiosks as kiosk (kiosk.id)}
          <Button variant="outline" class="w-full justify-start" onclick={() => sendToKiosk(kiosk)}>
            <Send class="mr-2 size-4" />{kiosk.name}
          </Button>
        {/each}
      {:else}
        <p class="text-sm text-muted-foreground">{status || 'No kiosks are connected. Waiting…'}</p>
      {/if}

      {#if pendingUrl}
        <Button
          variant="ghost"
          class="w-full justify-start"
          onclick={bypass}
        >
          <ExternalLink class="mr-2 size-4" />Open in browser
        </Button>
      {/if}

      <details class="mt-4 rounded-md border p-2">
        <summary class="cursor-pointer select-none text-xs text-muted-foreground">Original URL</summary>
        <div class="mt-2 flex items-center gap-2">
          <input
            readonly
            value={pendingUrl}
            class="w-full rounded border bg-muted px-2 py-1 font-mono text-xs"
          />
          <Button variant="outline" size="sm" onclick={copyPendingUrl}>
            {copied ? 'Copied!' : 'Copy'}
          </Button>
        </div>
      </details>
    </CardContent>
  </Card>
</div>
