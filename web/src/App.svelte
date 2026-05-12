<script lang="ts">
  import { onMount } from "svelte";
  import Register from "$lib/components/Register.svelte";
  import AddToHomeScreen from "$lib/components/AddToHomeScreen.svelte";
  import { Toaster } from "$lib/components/ui/sonner/index.js";

  const isStandalone =
    (navigator as any).standalone === true ||
    window.matchMedia("(display-mode: standalone)").matches;

  const token = localStorage.getItem("kiosk-token");

  type View = "install" | "register" | "validating" | "reconnecting" | "waiting" | "signing";

  let view = $state<View>(!isStandalone ? "install" : token ? "validating" : "register");

  let kioskName = $state("");
  let signingUrl = $state("");
  let signingInitialLoad = true;

  function handleSigningLoad() {
    if (signingInitialLoad) {
      signingInitialLoad = false;
      return;
    }
    signingUrl = "";
    view = "waiting";
  }

  onMount(() => {
    if (view !== "validating") return;

    let reconnectTimer: number;

    function connect() {
      const protocol = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(`${protocol}//${location.host}/ws?token=${token}`);
      let authenticated = false;

      ws.onerror = () => {};

      ws.onclose = () => {
        if (authenticated) {
          view = "reconnecting";
          reconnectTimer = window.setTimeout(connect, 3000);
        } else {
          localStorage.removeItem("kiosk-token");
          view = "register";
        }
      };

      ws.onmessage = ({ data }) => {
        const msg = JSON.parse(data);
        if (msg.type === "connected") {
          authenticated = true;
          kioskName = msg.name;
          view = "waiting";
        } else if (msg.type === "sign") {
          signingUrl = msg.url;
          signingInitialLoad = true;
          view = "signing";
        }
      };
    }

    connect();

    return () => clearTimeout(reconnectTimer);
  });
</script>

<Toaster position="top-center" />

{#if view === "install"}
  <AddToHomeScreen />
{:else if view === "register"}
  <Register />
{:else if view === "reconnecting"}
  <div class="flex min-h-svh flex-col items-center justify-center gap-2 bg-muted">
    <p class="text-2xl font-medium text-muted-foreground">Reconnecting…</p>
  </div>
{:else if view === "validating"}
  <!-- intentionally blank while connecting -->
{:else if view === "signing"}
  <iframe src={signingUrl} title="DocuSign" onload={handleSigningLoad} style="position:fixed;inset:0;width:100%;height:100%;border:none;"></iframe>
{:else}
  <div class="flex min-h-svh flex-col items-center justify-center gap-2 bg-muted">
    <p class="text-2xl font-medium text-muted-foreground">Ready for member</p>
    {#if kioskName}
      <p class="text-sm text-muted-foreground">{kioskName}</p>
    {/if}
  </div>
{/if}
