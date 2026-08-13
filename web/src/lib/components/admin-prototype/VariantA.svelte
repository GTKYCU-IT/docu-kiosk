<script lang="ts">
  import MoreHorizontal from "@lucide/svelte/icons/ellipsis";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import TriangleAlert from "@lucide/svelte/icons/triangle-alert";
  import X from "@lucide/svelte/icons/x";
  import type { PrototypeKiosk, VariantProps } from "./model";
  import { statusLabel } from "./model";

  let { kiosks, renameKiosk, deleteKiosk }: VariantProps = $props();

  let menuFor = $state<string | null>(null);
  let action = $state<{ kind: "rename" | "delete"; kiosk: PrototypeKiosk } | null>(null);
  let proposedName = $state("");
  let error = $state("");

  function beginRename(kiosk: PrototypeKiosk) {
    menuFor = null;
    proposedName = kiosk.name;
    error = "";
    action = { kind: "rename", kiosk };
  }

  function beginDelete(kiosk: PrototypeKiosk) {
    menuFor = null;
    error = "";
    action = { kind: "delete", kiosk };
  }

  function saveRename() {
    if (!action || action.kind !== "rename") return;
    error = renameKiosk(action.kiosk.id, proposedName) ?? "";
    if (!error) action = null;
  }

  function confirmDelete() {
    if (!action || action.kind !== "delete") return;
    error = deleteKiosk(action.kiosk.id) ?? "";
    if (!error) action = null;
  }
</script>

<section class="mx-auto max-w-6xl px-6 pb-32 pt-10">
  <header class="mb-7 flex flex-wrap items-end justify-between gap-4">
    <div>
      <p class="mb-2 text-xs font-bold uppercase tracking-[0.18em] text-slate-500">Kiosk administration</p>
      <h1 class="text-3xl font-semibold tracking-tight text-slate-950">Kiosk directory</h1>
      <p class="mt-2 text-sm text-slate-600">{kiosks.length} registered kiosks · {kiosks.filter((kiosk) => kiosk.status !== "offline").length} online</p>
    </div>
    <button type="button" class="rounded-lg border border-slate-300 bg-white px-3.5 py-2 text-sm font-semibold text-slate-700 shadow-sm hover:bg-slate-50">Refresh</button>
  </header>

  <div class="overflow-visible rounded-xl border border-slate-200 bg-white shadow-sm">
    <table class="w-full border-collapse text-left">
      <thead>
        <tr class="border-b border-slate-200 bg-slate-50/80 text-xs font-semibold uppercase tracking-wide text-slate-500">
          <th class="px-5 py-3.5">Kiosk</th>
          <th class="px-5 py-3.5">IP address</th>
          <th class="px-5 py-3.5">Status</th>
          <th class="px-5 py-3.5">Last activity</th>
          <th class="w-16 px-5 py-3.5"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each kiosks as kiosk (kiosk.id)}
          <tr class="border-b border-slate-100 last:border-0 hover:bg-slate-50/60">
            <td class="px-5 py-4 font-semibold text-slate-900">{kiosk.name}</td>
            <td class="px-5 py-4 font-mono text-sm text-slate-600">{kiosk.ip}</td>
            <td class="px-5 py-4">
              <span class={kiosk.status === "offline" ? "inline-flex items-center gap-2 rounded-full bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-600" : kiosk.status === "signing" ? "inline-flex items-center gap-2 rounded-full bg-blue-50 px-2.5 py-1 text-xs font-semibold text-blue-700" : "inline-flex items-center gap-2 rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-semibold text-emerald-700"}>
                <span class={kiosk.status === "offline" ? "size-1.5 rounded-full bg-slate-400" : kiosk.status === "signing" ? "size-1.5 rounded-full bg-blue-500" : "size-1.5 rounded-full bg-emerald-500"}></span>
                {statusLabel(kiosk.status)}
              </span>
            </td>
            <td class="px-5 py-4 text-sm text-slate-600">{kiosk.lastSeen}</td>
            <td class="relative px-5 py-4 text-right">
              <button type="button" aria-label={`Actions for ${kiosk.name}`} class="grid size-8 place-items-center rounded-md text-slate-500 hover:bg-slate-100 hover:text-slate-900" onclick={() => menuFor = menuFor === kiosk.id ? null : kiosk.id}>
                <MoreHorizontal class="size-4" />
              </button>
              {#if menuFor === kiosk.id}
                <div class="absolute right-5 top-12 z-10 w-44 rounded-lg border border-slate-200 bg-white p-1.5 text-left shadow-xl">
                  <button type="button" class="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm font-medium hover:bg-slate-100" onclick={() => beginRename(kiosk)}><Pencil class="size-4" /> Rename</button>
                  <button type="button" class="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm font-medium text-red-700 hover:bg-red-50" onclick={() => beginDelete(kiosk)}><Trash2 class="size-4" /> Delete kiosk</button>
                </div>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>

{#if action}
  <div class="fixed inset-0 z-40 grid place-items-center bg-slate-950/45 p-4" role="presentation">
    <div role="dialog" aria-modal="true" aria-labelledby="action-title" class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-2xl">
      <div class="flex items-start justify-between gap-4">
        <div>
          <p class="text-xs font-bold uppercase tracking-[0.16em] text-slate-500">{action.kiosk.ip}</p>
          <h2 id="action-title" class="mt-1 text-xl font-semibold text-slate-950">{action.kind === "rename" ? `Rename ${action.kiosk.name}` : `Delete ${action.kiosk.name}?`}</h2>
        </div>
        <button type="button" aria-label="Close" class="grid size-8 place-items-center rounded-lg text-slate-500 hover:bg-slate-100" onclick={() => action = null}><X class="size-4" /></button>
      </div>

      {#if action.kind === "rename"}
        <p class="mt-3 text-sm leading-6 text-slate-600">Its identity, IP address, and Kiosk session stay unchanged. If a member is signing, their Signing session will not be interrupted.</p>
        <label class="mt-5 block text-sm font-semibold text-slate-800" for="table-kiosk-name">Kiosk name</label>
        <input id="table-kiosk-name" class="mt-2 w-full rounded-lg border border-slate-300 px-3 py-2.5 text-sm outline-none focus:border-slate-600 focus:ring-2 focus:ring-slate-200" bind:value={proposedName} onkeydown={(event) => event.key === "Enter" && saveRename()} />
      {:else}
        <div class="mt-5 rounded-xl border border-red-200 bg-red-50 p-4">
          <div class="flex gap-3">
            <TriangleAlert class="mt-0.5 size-5 shrink-0 text-red-700" />
            <div class="text-sm leading-6 text-red-950">
              <p class="font-semibold">This permanently ends the kiosk identity.</p>
              {#if action.kiosk.status === "signing"}
                <p class="mt-1">The kiosk disconnects now, but the member’s current Signing session stays open until they finish. The kiosk then returns to registration.</p>
              {:else if action.kiosk.status === "ready"}
                <p class="mt-1">The kiosk disconnects immediately and returns to registration.</p>
              {:else}
                <p class="mt-1">If this kiosk reconnects, it returns to registration as a new kiosk.</p>
              {/if}
            </div>
          </div>
        </div>
      {/if}

      {#if error}<p role="alert" class="mt-4 rounded-lg bg-red-50 px-3 py-2 text-sm font-medium text-red-800">{error}</p>{/if}

      <div class="mt-6 flex justify-end gap-2">
        <button type="button" class="rounded-lg border border-slate-300 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50" onclick={() => action = null}>Cancel</button>
        {#if action.kind === "rename"}
          <button type="button" class="rounded-lg bg-slate-950 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-800" onclick={saveRename}>Save name</button>
        {:else}
          <button type="button" class="rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white hover:bg-red-700" onclick={confirmDelete}>Delete {action.kiosk.name}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}
