<script lang="ts">
  import { onMount } from 'svelte'

  const KIOSK_ID = new URLSearchParams(location.search).get('id') ?? 'unknown'
  const WS_URL = `ws://${location.hostname}:8080/kiosk/${KIOSK_ID}/ws`

  let signing = false

  onMount(() => {
    const ws = new WebSocket(WS_URL)

    ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data)
      if (msg.type === 'sign') {
        signing = true
        // TODO: load signing URL
      }
    }
  })
</script>

{#if signing}
  <!-- signing view goes here -->
{:else}
  <div class="waiting">Ready for member</div>
{/if}

<style>
  .waiting {
    display: flex;
    justify-content: center;
    align-items: center;
    height: 100vh;
    font-size: 2rem;
    font-family: sans-serif;
    color: #333;
    background: #f5f5f5;
  }
</style>
