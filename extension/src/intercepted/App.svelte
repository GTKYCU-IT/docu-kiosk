<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import { getConfig } from '../config'
  import {
    INTERCEPT_HASH_PREFIX,
    requestBypass,
    BypassResponseError,
    BrokerClient,
    type Kiosk,
    type BrokerState,
  } from '../lib/broker'
  import { Button } from '$lib/components/ui/button'
  import { ExternalLink, Send } from '@lucide/svelte'
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card'
  import { toast } from 'svelte-sonner'

  let status = $state('')
  let kiosks = $state<Kiosk[]>([])
  let sending = $state(false)
  let copied = $state(false)
  let pendingUrl = $state('')
  let client: BrokerClient | undefined

  onMount(async () => {
    const hash = window.location.hash
    if (!hash.startsWith(INTERCEPT_HASH_PREFIX)) {
      status = 'No signing link intercepted.'
      return
    }

    pendingUrl = hash.slice(INTERCEPT_HASH_PREFIX.length)

    const cfg = await getConfig()
    const brokerUrl = cfg.brokerUrl ?? ''

    if (!brokerUrl) {
      status = 'Broker URL not configured. Open the extension options to set it.'
      return
    }

    client = new BrokerClient({
      url: brokerUrl,
      onChange: applyState,
    })
    // The broker owns the listing loop: one immediate request, then
    // recurring polling with dedup. Polling pauses while a push is in
    // flight and resumes automatically if a push fails.
    client.listKiosks()
  })

  onDestroy(() => client?.close())

  // Translate the broker's typed state into the page's render state. The
  // statuses mirror the strings the page used before the client existed:
  // loading keeps the neutral waiting text, unreachable keeps the last
  // kiosk list and shows the reachability error, and ready renders the
  // kiosk list or the empty state.
  function applyState(state: BrokerState) {
    if (state.status === 'loading') {
      status = ''
    } else if (state.status === 'unreachable') {
      status = 'Could not reach broker. Check that it is running.'
    } else {
      kiosks = state.kiosks
      status = state.kiosks.length === 0 ? 'No kiosks are connected. Waiting…' : 'Select a kiosk:'
    }
  }

  async function sendToKiosk(kiosk: Kiosk) {
    if (!client) return
    sending = true
    status = `Sending to ${kiosk.name}…`
    try {
      await client.push(kiosk.id, pendingUrl)
      window.close()
    } catch {
      status = `Failed to send to ${kiosk.name}.`
      sending = false
    }
  }

  async function bypass() {
    // requestBypass owns the wake-retry protocol; map its outcomes to the
    // existing user-facing strings.
    try {
      await requestBypass(pendingUrl)
    } catch (err) {
      await copyPendingUrl()
      if (err instanceof BypassResponseError) {
        const msg = `Bypass failed: ${err.message}`
        console.error('[docu-kiosk]', msg)
        toast.error(msg)
      } else {
        console.error('[docu-kiosk] bypass:', err instanceof Error ? err.message : err)
        toast.error('Could not open in browser. The URL has been copied to your clipboard.')
      }
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

      {#if pendingUrl}
        <Button
          variant="ghost"
          class="w-full justify-start"
          onclick={bypass}
        >
          <ExternalLink class="mr-2 size-4" />Open in browser
        </Button>
      {/if}
    </CardContent>
  </Card>
</div>
