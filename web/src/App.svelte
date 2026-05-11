<script lang="ts">
  import { onMount } from 'svelte'
  import Register from '$lib/components/Register.svelte'

  const kioskId = localStorage.getItem('kioskId')
  const kioskToken = localStorage.getItem('kioskToken')
  const registered = !!(kioskId && kioskToken)

  let signing = $state(false)

  onMount(() => {
    if (!registered) return

    const ws = new WebSocket(`ws://${location.hostname}:8080/ws`)

    ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data)
      if (msg.type === 'sign') {
        signing = true
        // TODO: load signing URL into iframe
      }
    }
  })
</script>

{#if !registered}
  <Register />
{:else if signing}
  <!-- signing view goes here -->
{:else}
  <div class="flex min-h-svh items-center justify-center bg-muted">
    <p class="text-2xl font-medium text-muted-foreground">Ready for member</p>
  </div>
{/if}
