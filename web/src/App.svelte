<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import Register from "$lib/components/Register.svelte";
  import AddToHomeScreen from "$lib/components/AddToHomeScreen.svelte";
  import { Toaster } from "$lib/components/ui/sonner/index.js";
  import { Button } from "$lib/components/ui/button";
  import RefreshCCW from "@lucide/svelte/icons/refresh-ccw";
  import { BrokerConnection, type BrokerState, type BrokerStatus } from "$lib/broker";

  const isStandalone =
    (navigator as any).standalone === true ||
    window.matchMedia("(display-mode: standalone)").matches;

  type View =
    | "install"
    | "register"
    | "validating"
    | "reconnecting"
    | "waiting"
    | "signing";

  const statusToView: Record<BrokerStatus, View> = {
    connecting: "validating",
    unregistered: "register",
    ready: "waiting",
    reconnecting: "reconnecting",
    signing: "signing",
  };

  let view = $state<View>("validating");

  let kioskName = $state("");
  let signingUrl = $state("");
  let signingInitialLoad = true;

  // Broker build version, shown as a subtle footer label. Loaded silently:
  // if the fetch fails there is nothing to show and the kiosk carries on.
  let brokerVersion = $state("");

  let broker: BrokerConnection;

  onMount(() => {
    fetch("/api/version")
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { version?: string } | null) => {
        if (d?.version) brokerVersion = d.version;
      })
      .catch(() => {});
  });

  function handleSigningLoad() {
    if (signingInitialLoad) {
      signingInitialLoad = false;
      return;
    }
    broker.finishSigning();
  }

  // The register response reported this kiosk is already registered (e.g. the
  // original 204 was lost). Reopen the session immediately: the view moves
  // through "connecting" and the greeting on the fresh socket supplies the
  // authoritative kiosk name.
  function handleAlreadyRegistered() {
    broker.reopen();
  }

  onMount(() => {
    broker = new BrokerConnection({
      url: `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/ws`,
      onChange: (s: BrokerState) => {
        view = statusToView[s.status];
        if (s.kioskName !== undefined) kioskName = s.kioskName;
        if (s.status === "signing") {
          if (s.signingUrl !== undefined) signingUrl = s.signingUrl;
          signingInitialLoad = true;
        } else if (signingUrl) {
          signingUrl = "";
        }
      },
    })
  });

  onDestroy(() => broker.close());

  let spinning = $state(false);
</script>

<Toaster position="top-center" />

<!-- Subtle build-version footer; hidden while signing so it never overlays
     the DocuSign iframe, and on first-run/register screens. -->
{#if brokerVersion && view !== "signing" && view !== "register" && view !== "install"}
  <p
    class="pointer-events-none fixed inset-x-0 bottom-1 z-10 text-center text-xs font-medium text-muted-foreground/40"
  >
    docu-kiosk {brokerVersion}
  </p>
{/if}

{#if view === "install"}
  <AddToHomeScreen />
{:else if view === "register"}
  <Register onAlreadyRegistered={handleAlreadyRegistered} />
{:else if view === "reconnecting"}
  <div
    class="flex min-h-svh flex-col items-center justify-center gap-2 bg-muted"
  >
    <p class="text-2xl font-medium text-muted-foreground">Reconnecting…</p>
  </div>
{:else if view === "validating"}
  <!-- intentionally blank while connecting -->
{:else if view === "signing"}
  <iframe
    src={signingUrl}
    title="DocuSign"
    onload={handleSigningLoad}
    style="position:fixed;inset:0;width:100%;height:100%;border:none;"
  ></iframe>
{:else}
  <div
    class="flex min-h-svh flex-col items-center justify-center gap-2 bg-muted"
  >
    <p class="text-2xl font-medium text-muted-foreground">Ready for member</p>
    {#if kioskName}
      <p class="text-sm text-muted-foreground">{kioskName}</p>
    {/if}

    <Button
      variant="outline"
      class="mt-8"
      type="button"
      onclick={() => {
        view = "reconnecting";
        setTimeout(() => window.location.reload(), 250);
      }}
    >
      <RefreshCCW class={["size-4", { "animate-spin": spinning }]} />
    </Button>
  </div>
{/if}
