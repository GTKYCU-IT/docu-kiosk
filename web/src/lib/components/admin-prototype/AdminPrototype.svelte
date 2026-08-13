<!-- Three variants of Kiosk administration, switchable via ?variant=, on the throwaway /admin/prototype route. -->
<script lang="ts">
  import RotateCcw from "@lucide/svelte/icons/rotate-ccw";
  import TriangleAlert from "@lucide/svelte/icons/triangle-alert";
  import VariantA from "./VariantA.svelte";
  import VariantB from "./VariantB.svelte";
  import VariantC from "./VariantC.svelte";
  import PrototypeSwitcher from "./PrototypeSwitcher.svelte";
  import { cloneFixtures, type DirectoryState, type PrototypeKiosk } from "./model";

  type Variant = "A" | "B" | "C";

  const initialParams = new URLSearchParams(location.search);
  const requestedVariant = initialParams.get("variant");
  const requestedState = initialParams.get("state");

  let variant = $state<Variant>(requestedVariant === "B" || requestedVariant === "C" ? requestedVariant : "A");
  let directoryState = $state<DirectoryState>(requestedState === "loading" || requestedState === "empty" || requestedState === "error" ? requestedState : "ready");
  let kiosks = $state<PrototypeKiosk[]>(cloneFixtures());
  let failNextMutation = $state(false);
  let result = $state("No changes yet");

  function replaceQuery(nextVariant = variant, nextState = directoryState) {
    const params = new URLSearchParams(location.search);
    params.set("variant", nextVariant);
    params.set("state", nextState);
    history.replaceState(null, "", `${location.pathname}?${params}`);
  }

  function changeVariant(next: Variant) {
    variant = next;
    result = `Viewing variant ${next}`;
    replaceQuery(next, directoryState);
  }

  function changeDirectoryState(next: DirectoryState) {
    directoryState = next;
    result = `Directory state changed to ${next}`;
    replaceQuery(variant, next);
  }

  function normalizedName(name: string) {
    return name.trim().toLocaleLowerCase();
  }

  function renameKiosk(id: string, name: string): string | null {
    const trimmed = name.trim();
    if (!trimmed) return "Enter a kiosk name.";
    if (kiosks.some((kiosk) => kiosk.id !== id && normalizedName(kiosk.name) === normalizedName(trimmed))) {
      return `A kiosk named “${trimmed}” already exists.`;
    }
    if (failNextMutation) {
      failNextMutation = false;
      result = "Rename failed; the directory is unchanged";
      return "The Broker could not save the name. No changes were made. Try again.";
    }
    const previous = kiosks.find((kiosk) => kiosk.id === id);
    kiosks = kiosks.map((kiosk) => kiosk.id === id ? { ...kiosk, name: trimmed } : kiosk);
    result = `${previous?.name ?? "Kiosk"} renamed to ${trimmed}`;
    return null;
  }

  function deleteKiosk(id: string): string | null {
    if (failNextMutation) {
      failNextMutation = false;
      result = "Delete failed; the kiosk remains in the directory";
      return "The Broker could not delete this kiosk. It remains registered. Try again.";
    }
    const deleted = kiosks.find((kiosk) => kiosk.id === id);
    kiosks = kiosks.filter((kiosk) => kiosk.id !== id);
    result = `${deleted?.name ?? "Kiosk"} deleted; its identity and Kiosk session ended`;
    return null;
  }

  function reset() {
    kiosks = cloneFixtures();
    failNextMutation = false;
    result = "Prototype reset";
  }

  function retryLoad() {
    changeDirectoryState("loading");
    window.setTimeout(() => changeDirectoryState("ready"), 650);
  }
</script>

<svelte:head><title>Kiosk administration prototype</title></svelte:head>

<div class="sticky top-0 z-30 border-b border-amber-300 bg-amber-50 px-4 py-2.5 shadow-sm">
  <div class="mx-auto flex max-w-7xl flex-wrap items-center gap-x-5 gap-y-2 text-xs text-amber-950">
    <span class="rounded bg-amber-950 px-2 py-1 font-bold uppercase tracking-[0.16em] text-amber-50">Prototype · not production</span>
    <label class="flex items-center gap-2 font-semibold">Directory state
      <select aria-label="Directory state" class="rounded-md border border-amber-300 bg-white px-2 py-1.5" value={directoryState} onchange={(event) => changeDirectoryState(event.currentTarget.value as DirectoryState)}>
        <option value="ready">Ready</option><option value="loading">Loading</option><option value="empty">Empty</option><option value="error">Load failed</option>
      </select>
    </label>
    <label class="flex items-center gap-2 font-semibold"><input type="checkbox" bind:checked={failNextMutation} /> Fail next save or delete</label>
    <button type="button" class="inline-flex items-center gap-1.5 rounded-md px-2 py-1.5 font-semibold hover:bg-amber-100" onclick={reset}><RotateCcw class="size-3.5" /> Reset</button>
    <span class="ml-auto text-amber-800"><strong>State:</strong> {variant} / {directoryState} / {kiosks.length} entries / {failNextMutation ? "next mutation fails" : "mutations succeed"} / {result}</span>
  </div>
</div>

{#if result !== "No changes yet" && !result.startsWith("Viewing") && !result.startsWith("Directory state") && result !== "Prototype reset"}
  <div role="status" class={result.includes("failed") ? "mx-auto mt-5 max-w-6xl rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm font-semibold text-red-800" : "mx-auto mt-5 max-w-6xl rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm font-semibold text-emerald-800"}>{result}</div>
{/if}

{#if directoryState === "loading"}
  <main class="mx-auto max-w-6xl px-6 pb-32 pt-12" aria-busy="true">
    <div class="h-4 w-40 animate-pulse rounded bg-slate-200"></div><div class="mt-3 h-9 w-72 animate-pulse rounded bg-slate-200"></div>
    <div class="mt-8 overflow-hidden rounded-xl border border-slate-200 bg-white">
      {#each Array(5) as _}<div class="grid grid-cols-4 gap-6 border-b border-slate-100 px-5 py-5 last:border-0"><span class="h-4 animate-pulse rounded bg-slate-200"></span><span class="h-4 animate-pulse rounded bg-slate-100"></span><span class="h-4 animate-pulse rounded bg-slate-100"></span><span class="h-4 animate-pulse rounded bg-slate-100"></span></div>{/each}
    </div>
    <p class="sr-only">Loading kiosk directory</p>
  </main>
{:else if directoryState === "empty"}
  <main class="grid min-h-[70vh] place-items-center px-6 pb-32 text-center">
    <div class="max-w-md"><div class="mx-auto grid size-14 place-items-center rounded-2xl bg-slate-100 text-2xl">0</div><h1 class="mt-5 text-2xl font-semibold text-slate-950">No registered kiosks</h1><p class="mt-2 text-sm leading-6 text-slate-600">Kiosks appear here after they register themselves from the kiosk screen. Administrators do not create them here.</p><button type="button" class="mt-5 rounded-lg border border-slate-300 bg-white px-4 py-2 text-sm font-semibold shadow-sm">Refresh directory</button></div>
  </main>
{:else if directoryState === "error"}
  <main class="grid min-h-[70vh] place-items-center px-6 pb-32 text-center">
    <div class="max-w-md"><div class="mx-auto grid size-14 place-items-center rounded-2xl bg-red-50 text-red-700"><TriangleAlert class="size-6" /></div><h1 class="mt-5 text-2xl font-semibold text-slate-950">Kiosk directory unavailable</h1><p class="mt-2 text-sm leading-6 text-slate-600">We could not load the directory. No kiosks were changed. Check your connection and try again.</p><button type="button" class="mt-5 rounded-lg bg-slate-950 px-4 py-2 text-sm font-semibold text-white" onclick={retryLoad}>Try again</button></div>
  </main>
{:else if variant === "A"}
  <VariantA {kiosks} {renameKiosk} {deleteKiosk} />
{:else if variant === "B"}
  <VariantB {kiosks} {renameKiosk} {deleteKiosk} />
{:else}
  <VariantC {kiosks} {renameKiosk} {deleteKiosk} />
{/if}

<PrototypeSwitcher current={variant} change={changeVariant} />
