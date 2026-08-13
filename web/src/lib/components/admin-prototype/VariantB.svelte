<script lang="ts">
  import Check from "@lucide/svelte/icons/check";
  import Monitor from "@lucide/svelte/icons/monitor";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import TriangleAlert from "@lucide/svelte/icons/triangle-alert";
  import type { VariantProps } from "./model";
  import { statusLabel } from "./model";

  let { kiosks, renameKiosk, deleteKiosk }: VariantProps = $props();

  let selectedId = $state("");
  let panel = $state<"details" | "rename" | "delete">("details");
  let proposedName = $state("");
  let typedName = $state("");
  let error = $state("");
  let selected = $derived(kiosks.find((kiosk) => kiosk.id === selectedId) ?? kiosks[0]);

  function choose(id: string) {
    selectedId = id;
    panel = "details";
    error = "";
  }

  function beginRename() {
    if (!selected) return;
    proposedName = selected.name;
    error = "";
    panel = "rename";
  }

  function saveRename() {
    if (!selected) return;
    error = renameKiosk(selected.id, proposedName) ?? "";
    if (!error) panel = "details";
  }

  function beginDelete() {
    typedName = "";
    error = "";
    panel = "delete";
  }

  function confirmDelete() {
    if (!selected) return;
    if (typedName !== selected.name) {
      error = `Type “${selected.name}” exactly to continue.`;
      return;
    }
    error = deleteKiosk(selected.id) ?? "";
    if (!error) {
      panel = "details";
      selectedId = kiosks.find((kiosk) => kiosk.id !== selected.id)?.id ?? "";
    }
  }
</script>

<section class="min-h-[calc(100vh-3rem)] bg-slate-100 pb-32">
  <header class="border-b border-slate-200 bg-white px-6 py-5">
    <div class="mx-auto flex max-w-6xl items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <div class="grid size-10 place-items-center rounded-xl bg-indigo-600 text-white"><Monitor class="size-5" /></div>
        <div>
          <h1 class="text-lg font-semibold text-slate-950">Kiosk administration</h1>
          <p class="text-sm text-slate-500">Select a kiosk to inspect or change it</p>
        </div>
      </div>
      <button type="button" class="text-sm font-semibold text-slate-600 hover:text-slate-950">Sign out</button>
    </div>
  </header>

  <div class="mx-auto grid max-w-6xl grid-cols-[minmax(17rem,0.78fr)_minmax(28rem,1.4fr)] gap-5 px-6 py-7">
    <aside class="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm">
      <div class="border-b border-slate-200 px-4 py-3">
        <p class="text-xs font-bold uppercase tracking-[0.16em] text-slate-500">All kiosks</p>
      </div>
      <div class="p-2">
        {#each kiosks as kiosk (kiosk.id)}
          <button type="button" class={selected?.id === kiosk.id ? "mb-1 flex w-full items-center gap-3 rounded-xl bg-indigo-50 px-3 py-3 text-left ring-1 ring-indigo-100 last:mb-0" : "mb-1 flex w-full items-center gap-3 rounded-xl px-3 py-3 text-left hover:bg-slate-50 last:mb-0"} onclick={() => choose(kiosk.id)}>
            <span class={kiosk.status === "offline" ? "size-2.5 shrink-0 rounded-full bg-slate-300" : kiosk.status === "signing" ? "size-2.5 shrink-0 rounded-full bg-blue-500 ring-4 ring-blue-100" : "size-2.5 shrink-0 rounded-full bg-emerald-500 ring-4 ring-emerald-100"}></span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-semibold text-slate-900">{kiosk.name}</span>
              <span class="mt-0.5 block text-xs text-slate-500">{statusLabel(kiosk.status)}</span>
            </span>
            {#if selected?.id === kiosk.id}<Check class="size-4 text-indigo-600" />{/if}
          </button>
        {/each}
      </div>
    </aside>

    {#if selected}
      <main class="rounded-2xl border border-slate-200 bg-white shadow-sm">
        <div class="border-b border-slate-200 px-7 py-6">
          <div class="flex items-start justify-between gap-5">
            <div>
              <p class="font-mono text-xs font-semibold text-slate-500">{selected.ip}</p>
              <h2 class="mt-1 text-2xl font-semibold tracking-tight text-slate-950">{selected.name}</h2>
            </div>
            <span class={selected.status === "offline" ? "rounded-full bg-slate-100 px-3 py-1.5 text-xs font-bold text-slate-600" : selected.status === "signing" ? "rounded-full bg-blue-50 px-3 py-1.5 text-xs font-bold text-blue-700" : "rounded-full bg-emerald-50 px-3 py-1.5 text-xs font-bold text-emerald-700"}>{statusLabel(selected.status)}</span>
          </div>
        </div>

        <div class="p-7">
          {#if panel === "details"}
            <dl class="grid grid-cols-2 gap-x-8 gap-y-6">
              <div><dt class="text-xs font-bold uppercase tracking-wide text-slate-500">IP address</dt><dd class="mt-1.5 font-mono text-sm text-slate-900">{selected.ip}</dd></div>
              <div><dt class="text-xs font-bold uppercase tracking-wide text-slate-500">Activity</dt><dd class="mt-1.5 text-sm text-slate-900">{selected.lastSeen}</dd></div>
              <div><dt class="text-xs font-bold uppercase tracking-wide text-slate-500">Directory identity</dt><dd class="mt-1.5 text-sm text-slate-900">Registered</dd></div>
              <div><dt class="text-xs font-bold uppercase tracking-wide text-slate-500">Current use</dt><dd class="mt-1.5 text-sm text-slate-900">{selected.status === "signing" ? "Member is signing" : selected.status === "ready" ? "Ready for member" : "Not connected"}</dd></div>
            </dl>

            {#if selected.status === "signing"}
              <div class="mt-7 rounded-xl border border-blue-200 bg-blue-50 p-4 text-sm leading-6 text-blue-950"><strong>Signing session in progress.</strong> Renaming is safe and appears immediately. Deleting disconnects this kiosk, but the member’s Signing session remains usable until they finish.</div>
            {/if}

            <div class="mt-8 flex items-center gap-3 border-t border-slate-200 pt-6">
              <button type="button" class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-indigo-700" onclick={beginRename}><Pencil class="size-4" /> Rename kiosk</button>
              <button type="button" class="inline-flex items-center gap-2 rounded-lg px-3 py-2.5 text-sm font-semibold text-red-700 hover:bg-red-50" onclick={beginDelete}><Trash2 class="size-4" /> Delete</button>
            </div>
          {:else if panel === "rename"}
            <div class="max-w-lg">
              <h3 class="text-lg font-semibold text-slate-950">Rename kiosk</h3>
              <p class="mt-2 text-sm leading-6 text-slate-600">The IP address and kiosk identity do not change. Any live or Signing session continues without interruption.</p>
              <label for="focus-name" class="mt-5 block text-sm font-semibold text-slate-800">New kiosk name</label>
              <input id="focus-name" class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2.5 text-sm outline-none focus:border-indigo-500 focus:ring-2 focus:ring-indigo-100" bind:value={proposedName} />
              {#if error}<p role="alert" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-sm font-medium text-red-800">{error}</p>{/if}
              <div class="mt-6 flex gap-2"><button type="button" class="rounded-lg bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white" onclick={saveRename}>Save name</button><button type="button" class="rounded-lg px-4 py-2.5 text-sm font-semibold text-slate-600 hover:bg-slate-100" onclick={() => panel = "details"}>Cancel</button></div>
            </div>
          {:else}
            <div class="max-w-xl">
              <div class="flex gap-4">
                <div class="grid size-10 shrink-0 place-items-center rounded-full bg-red-100 text-red-700"><TriangleAlert class="size-5" /></div>
                <div>
                  <h3 class="text-lg font-semibold text-slate-950">Permanently delete {selected.name}?</h3>
                  <p class="mt-2 text-sm leading-6 text-slate-600">This ends the kiosk identity and revokes its Kiosk session immediately. The IP address and name become available for a newly registered kiosk.</p>
                </div>
              </div>
              <div class="mt-5 rounded-xl border border-red-200 bg-red-50 p-4 text-sm leading-6 text-red-950">
                {#if selected.status === "signing"}<strong>The member’s current Signing session will stay open until they finish.</strong> Afterward, this kiosk returns to registration.
                {:else if selected.status === "ready"}<strong>This online kiosk disconnects now</strong> and returns to registration.
                {:else}<strong>This kiosk is offline.</strong> If it reconnects, it returns to registration as a new kiosk.{/if}
              </div>
              <label for="delete-name" class="mt-5 block text-sm font-semibold text-slate-800">Type <strong>{selected.name}</strong> to confirm</label>
              <input id="delete-name" class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2.5 text-sm outline-none focus:border-red-500 focus:ring-2 focus:ring-red-100" bind:value={typedName} />
              {#if error}<p role="alert" class="mt-3 rounded-lg bg-red-50 px-3 py-2 text-sm font-medium text-red-800">{error}</p>{/if}
              <div class="mt-6 flex gap-2"><button type="button" class="rounded-lg bg-red-600 px-4 py-2.5 text-sm font-semibold text-white disabled:cursor-not-allowed disabled:opacity-40" disabled={typedName !== selected.name} onclick={confirmDelete}>Permanently delete kiosk</button><button type="button" class="rounded-lg px-4 py-2.5 text-sm font-semibold text-slate-600 hover:bg-slate-100" onclick={() => panel = "details"}>Cancel</button></div>
            </div>
          {/if}
        </div>
      </main>
    {/if}
  </div>
</section>
