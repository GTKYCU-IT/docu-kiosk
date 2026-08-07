<script lang="ts">
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'

  let optionsBrokerUrl = $state('')
  let saved = $state(false)

  onMount(async () => {
    const data = await chrome.storage.local.get('brokerUrl') as { brokerUrl?: string }
    optionsBrokerUrl = data.brokerUrl ?? ''
  })

  async function saveOptions() {
    await chrome.storage.local.set({ brokerUrl: optionsBrokerUrl })
    saved = true
    setTimeout(() => { saved = false }, 2000)
  }
</script>

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
