<script lang="ts">
  import { onMount } from "svelte";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import ChevronRight from "@lucide/svelte/icons/chevron-right";

  interface Props {
    current: "A" | "B" | "C";
    change: (variant: "A" | "B" | "C") => void;
  }

  let { current, change }: Props = $props();

  const variants = ["A", "B", "C"] as const;
  const names = {
    A: "Directory table",
    B: "Focus panel",
    C: "Status lanes",
  } as const;

  function cycle(direction: -1 | 1) {
    const currentIndex = variants.indexOf(current);
    const nextIndex = (currentIndex + direction + variants.length) % variants.length;
    change(variants[nextIndex]);
  }

  onMount(() => {
    function handleKeydown(event: KeyboardEvent) {
      const target = event.target as HTMLElement | null;
      if (target?.matches("input, textarea, select, [contenteditable='true']")) return;
      if (event.key === "ArrowLeft") cycle(-1);
      if (event.key === "ArrowRight") cycle(1);
    }

    window.addEventListener("keydown", handleKeydown);
    return () => window.removeEventListener("keydown", handleKeydown);
  });
</script>

{#if import.meta.env.DEV}
  <nav
    aria-label="Prototype variants"
    class="fixed bottom-5 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1 rounded-full border border-slate-700 bg-slate-950 p-1.5 text-white shadow-2xl shadow-slate-950/30"
  >
    <button
      type="button"
      aria-label="Previous variant"
      class="grid size-9 place-items-center rounded-full hover:bg-white/15 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
      onclick={() => cycle(-1)}
    >
      <ChevronLeft class="size-4" />
    </button>
    <span class="min-w-44 px-3 text-center text-sm font-semibold">
      {current} — {names[current]}
    </span>
    <button
      type="button"
      aria-label="Next variant"
      class="grid size-9 place-items-center rounded-full hover:bg-white/15 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
      onclick={() => cycle(1)}
    >
      <ChevronRight class="size-4" />
    </button>
  </nav>
{/if}
