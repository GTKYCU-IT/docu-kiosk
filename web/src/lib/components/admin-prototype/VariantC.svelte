<script lang="ts">
  import CircleDot from "@lucide/svelte/icons/circle-dot";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import TriangleAlert from "@lucide/svelte/icons/triangle-alert";
  import type { PrototypeKiosk, VariantProps } from "./model";

  let { kiosks, renameKiosk, deleteKiosk }: VariantProps = $props();

  let action = $state<{ kind: "rename" | "delete"; id: string } | null>(null);
  let proposedName = $state("");
  let error = $state("");
  let online = $derived(kiosks.filter((kiosk) => kiosk.status !== "offline"));
  let offline = $derived(kiosks.filter((kiosk) => kiosk.status === "offline"));

  function openRename(kiosk: PrototypeKiosk) {
    proposedName = kiosk.name;
    error = "";
    action = { kind: "rename", id: kiosk.id };
  }

  function openDelete(kiosk: PrototypeKiosk) {
    error = "";
    action = { kind: "delete", id: kiosk.id };
  }

  function saveRename(kiosk: PrototypeKiosk) {
    error = renameKiosk(kiosk.id, proposedName) ?? "";
    if (!error) action = null;
  }

  function confirmDelete(kiosk: PrototypeKiosk) {
    error = deleteKiosk(kiosk.id) ?? "";
    if (!error) action = null;
  }
</script>

<section class="min-h-screen bg-[#f6f7f3] px-6 pb-32 pt-10 text-[#17201b]">
  <div class="mx-auto max-w-6xl">
    <header class="flex flex-wrap items-start justify-between gap-6 border-b border-[#d8ddd6] pb-7">
      <div>
        <div class="mb-3 inline-flex items-center gap-2 rounded-full bg-[#e1e9df] px-3 py-1 text-xs font-bold uppercase tracking-[0.15em] text-[#426046]"><CircleDot class="size-3.5" /> Directory overview</div>
        <h1 class="font-serif text-4xl font-semibold tracking-tight">Branch kiosks</h1>
        <p class="mt-2 max-w-xl text-sm leading-6 text-[#657069]">Online kiosks appear first so active sessions are obvious. Offline entries remain visible and manageable.</p>
      </div>
      <div class="flex gap-8 rounded-2xl border border-[#d8ddd6] bg-white px-6 py-4 shadow-sm">
        <div><p class="text-2xl font-semibold text-[#285d35]">{online.length}</p><p class="text-xs font-bold uppercase tracking-wide text-[#768078]">Online</p></div>
        <div class="border-l border-[#d8ddd6] pl-8"><p class="text-2xl font-semibold text-[#59635c]">{offline.length}</p><p class="text-xs font-bold uppercase tracking-wide text-[#768078]">Offline</p></div>
      </div>
    </header>

    <div class="mt-8 grid grid-cols-2 items-start gap-8">
      <section>
        <div class="mb-4 flex items-center gap-2"><span class="size-2.5 rounded-full bg-[#3b9b56] ring-4 ring-[#dcecdf]"></span><h2 class="text-sm font-bold uppercase tracking-[0.14em] text-[#426047]">Online now</h2></div>
        <div class="space-y-3">
          {#each online as kiosk (kiosk.id)}
            <article class={kiosk.status === "signing" ? "overflow-hidden rounded-2xl border border-[#adc7d8] bg-white shadow-sm" : "overflow-hidden rounded-2xl border border-[#d8ddd6] bg-white shadow-sm"}>
              <div class="p-5">
                <div class="flex items-start justify-between gap-4">
                  <div><h3 class="text-lg font-semibold">{kiosk.name}</h3><p class="mt-1 font-mono text-xs text-[#768078]">{kiosk.ip}</p></div>
                  <span class={kiosk.status === "signing" ? "rounded-full bg-[#e4f1fa] px-2.5 py-1 text-xs font-bold text-[#286487]" : "rounded-full bg-[#e5f3e7] px-2.5 py-1 text-xs font-bold text-[#28703c]"}>{kiosk.status === "signing" ? "Member signing" : "Ready"}</span>
                </div>
                <p class="mt-4 text-sm text-[#657069]">{kiosk.lastSeen}</p>
                <div class="mt-4 flex gap-2 border-t border-[#e7eae5] pt-4">
                  <button type="button" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-bold text-[#3e5943] hover:bg-[#edf1eb]" onclick={() => openRename(kiosk)}><Pencil class="size-3.5" /> Rename</button>
                  <button type="button" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-bold text-[#a13b35] hover:bg-red-50" onclick={() => openDelete(kiosk)}><Trash2 class="size-3.5" /> Delete</button>
                </div>
              </div>
              {#if action?.id === kiosk.id}
                <div class={action.kind === "delete" ? "border-t border-red-200 bg-red-50 p-5" : "border-t border-[#d8ddd6] bg-[#f5f7f3] p-5"}>
                  {#if action.kind === "rename"}
                    <p class="text-sm font-semibold">Rename {kiosk.name}</p>
                    <p class="mt-1 text-xs leading-5 text-[#657069]">The Kiosk session and any Signing session continue.</p>
                    <div class="mt-3 flex gap-2"><input aria-label="New kiosk name" class="min-w-0 flex-1 rounded-lg border border-[#c8cec6] bg-white px-3 py-2 text-sm outline-none focus:border-[#4f6954]" bind:value={proposedName} /><button type="button" class="rounded-lg bg-[#284c31] px-3 py-2 text-xs font-bold text-white" onclick={() => saveRename(kiosk)}>Save</button><button type="button" class="rounded-lg px-2 py-2 text-xs font-bold" onclick={() => action = null}>Cancel</button></div>
                  {:else}
                    <div class="flex gap-3"><TriangleAlert class="mt-0.5 size-5 shrink-0 text-red-700" /><div><p class="text-sm font-bold text-red-950">Delete this kiosk permanently?</p><p class="mt-1 text-xs leading-5 text-red-900">Its Kiosk session is revoked now. {kiosk.status === "signing" ? "The member can finish the current Signing session; registration appears afterward." : "Registration appears immediately."}</p></div></div>
                    <div class="mt-4 flex justify-end gap-2"><button type="button" class="rounded-lg px-3 py-2 text-xs font-bold text-red-800" onclick={() => action = null}>Keep kiosk</button><button type="button" class="rounded-lg bg-red-600 px-3 py-2 text-xs font-bold text-white" onclick={() => confirmDelete(kiosk)}>Disconnect & delete</button></div>
                  {/if}
                  {#if error}<p role="alert" class="mt-3 text-xs font-semibold text-red-800">{error}</p>{/if}
                </div>
              {/if}
            </article>
          {/each}
        </div>
      </section>

      <section>
        <div class="mb-4 flex items-center gap-2"><span class="size-2.5 rounded-full bg-[#aab0ab]"></span><h2 class="text-sm font-bold uppercase tracking-[0.14em] text-[#68716a]">Offline</h2></div>
        <div class="space-y-3">
          {#each offline as kiosk (kiosk.id)}
            <article class="overflow-hidden rounded-2xl border border-[#d8ddd6] bg-white/80 shadow-sm">
              <div class="p-5">
                <div class="flex items-start justify-between gap-4"><div><h3 class="text-lg font-semibold">{kiosk.name}</h3><p class="mt-1 font-mono text-xs text-[#768078]">{kiosk.ip}</p></div><span class="rounded-full bg-[#edf0ed] px-2.5 py-1 text-xs font-bold text-[#667068]">Offline</span></div>
                <p class="mt-4 text-sm text-[#657069]">{kiosk.lastSeen}</p>
                <div class="mt-4 flex gap-2 border-t border-[#e7eae5] pt-4"><button type="button" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-bold text-[#3e5943] hover:bg-[#edf1eb]" onclick={() => openRename(kiosk)}><Pencil class="size-3.5" /> Rename</button><button type="button" class="inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs font-bold text-[#a13b35] hover:bg-red-50" onclick={() => openDelete(kiosk)}><Trash2 class="size-3.5" /> Delete</button></div>
              </div>
              {#if action?.id === kiosk.id}
                <div class={action.kind === "delete" ? "border-t border-red-200 bg-red-50 p-5" : "border-t border-[#d8ddd6] bg-[#f5f7f3] p-5"}>
                  {#if action.kind === "rename"}
                    <p class="text-sm font-semibold">Rename {kiosk.name}</p><p class="mt-1 text-xs leading-5 text-[#657069]">The kiosk identity and fixed IP stay unchanged.</p><div class="mt-3 flex gap-2"><input aria-label="New kiosk name" class="min-w-0 flex-1 rounded-lg border border-[#c8cec6] bg-white px-3 py-2 text-sm outline-none" bind:value={proposedName} /><button type="button" class="rounded-lg bg-[#284c31] px-3 py-2 text-xs font-bold text-white" onclick={() => saveRename(kiosk)}>Save</button><button type="button" class="rounded-lg px-2 py-2 text-xs font-bold" onclick={() => action = null}>Cancel</button></div>
                  {:else}
                    <div class="flex gap-3"><TriangleAlert class="mt-0.5 size-5 shrink-0 text-red-700" /><div><p class="text-sm font-bold text-red-950">Delete this kiosk permanently?</p><p class="mt-1 text-xs leading-5 text-red-900">If it reconnects, it returns to registration as a new kiosk. This IP and name become available.</p></div></div><div class="mt-4 flex justify-end gap-2"><button type="button" class="rounded-lg px-3 py-2 text-xs font-bold text-red-800" onclick={() => action = null}>Keep kiosk</button><button type="button" class="rounded-lg bg-red-600 px-3 py-2 text-xs font-bold text-white" onclick={() => confirmDelete(kiosk)}>Delete kiosk</button></div>
                  {/if}
                  {#if error}<p role="alert" class="mt-3 text-xs font-semibold text-red-800">{error}</p>{/if}
                </div>
              {/if}
            </article>
          {/each}
        </div>
      </section>
    </div>
  </div>
</section>
