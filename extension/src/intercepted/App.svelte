<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { getConfig } from '../config'
  import { Button } from '$lib/components/ui/button'
  import { Send } from '@lucide/svelte'
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'

  type View = 'loading' | 'picker' | 'options'
  type Kiosk = { id: string; name: string }

  let view = $state<View>('loading')

  // picker state
  let status = $state('')
  let kiosks = $state<Kiosk[]>([])
  let sending = $state(false)
  let brokerUrl = ''
  let pendingUrl = ''
  let pollInterval: ReturnType<typeof setInterval> | undefined

  // options state
  let optionsBrokerUrl = $state('')
  let saved = $state(false)

  onMount(async () => {
    const session = await chrome.storage.session.get('pendingSigningUrl') as { pendingSigningUrl?: string }

    if (!session.pendingSigningUrl) {
      const data = await chrome.storage.local.get('brokerUrl') as { brokerUrl?: string }
      optionsBrokerUrl = data.brokerUrl ?? ''
      view = 'options'
      return
    }

    const cfg = await getConfig()
    brokerUrl = cfg.brokerUrl ?? ''
    pendingUrl = session.pendingSigningUrl

    if (!brokerUrl) {
      status = 'Broker URL not configured. Open extension options to set it.'
      view = 'picker'
      return
    }

    view = 'picker'
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
      await chrome.storage.session.remove('pendingSigningUrl')
      window.close()
    } catch {
      status = `Failed to send to ${kiosk.name}.`
      sending = false
      pollInterval = setInterval(refreshKiosks, 3000)
    }
  }

  async function saveOptions() {
    await chrome.storage.local.set({ brokerUrl: optionsBrokerUrl })
    saved = true
    setTimeout(() => { saved = false }, 2000)
  }
</script>

{#if view === 'picker'}
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
      </CardContent>
    </Card>
  </div>
{:else if view === 'options'}
  <div class="flex min-h-svh items-center justify-center bg-muted p-4">
    <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>DocuKiosk Settings</CardTitle>
      </CardHeader>
      <CardContent class="flex flex-col gap-4">
        <div class="flex flex-col gap-2">
          <Label for="brokerUrl">Broker URL</Label>
          <Input id="brokerUrl" type="url" placeholder="https://broker.internal" bind:value={optionsBrokerUrl} />
        </div>
        <Button onclick={saveOptions} class="w-full">
          {saved ? 'Saved!' : 'Save'}
        </Button>
      </CardContent>
    </Card>
  </div>
{/if}
