<script lang="ts">
  import { onMount } from "svelte";
  import Register from "$lib/components/Register.svelte";
  import AddToHomeScreen from "$lib/components/AddToHomeScreen.svelte";
  import { Toaster } from "$lib/components/ui/sonner/index.js";

  const params = new URLSearchParams(location.search);
  const token = params.get("token");
  const initial = params.has("initial");

  type View =
    | "register"
    | "validating"
    | "add-to-homescreen"
    | "waiting"
    | "signing";
  let view = $state<View>(token ? "validating" : "register");
  let kioskName = $state("");

  onMount(() => {
    if (!token) return;

    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${protocol}//${location.host}/ws?token=${token}`);

    ws.onerror = () => {
      view = "register";
    };

    ws.onmessage = ({ data }) => {
      const msg = JSON.parse(data);
      if (msg.type === "connected") {
        kioskName = msg.name;
        if (initial) {
          history.replaceState({}, "", `/?token=${token}`);
          view = "add-to-homescreen";
        } else {
          view = "waiting";
        }
      } else if (msg.type === "sign") {
        view = "signing";
      }
    };
  });
</script>

<Toaster position="top-center" />

{#if view === "register"}
  <Register />
{:else if view === "validating"}
  <!-- intentionally blank while validating -->
{:else if view === "add-to-homescreen"}
  <AddToHomeScreen {token} onDone={() => (view = "waiting")} />
{:else if view === "signing"}
  <!-- signing view placeholder -->
{:else}
  <div class="flex min-h-svh flex-col items-center justify-center gap-2 bg-muted">
    <p class="text-2xl font-medium text-muted-foreground">Ready for member</p>
    {#if kioskName}
      <p class="text-sm text-muted-foreground">{kioskName}</p>
    {/if}
  </div>
{/if}
