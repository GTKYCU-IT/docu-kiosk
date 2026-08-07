<script lang="ts">
  import { Toaster as Sonner, type ToasterProps as SonnerProps } from 'svelte-sonner'

  let { ...restProps }: SonnerProps = $props()
</script>

<Sonner
  class="toaster group"
  style="
    --normal-bg: var(--color-popover);
    --normal-text: var(--color-popover-foreground);
    --normal-border: var(--color-border);
    --error-bg: var(--color-destructive);
    --error-text: var(--color-destructive-foreground);
    --error-border: var(--color-destructive);
  "
  {...restProps}
/>
