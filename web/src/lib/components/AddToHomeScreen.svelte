<script lang="ts">
  import * as Card from "$lib/components/ui/card";
  import { Button } from "$lib/components/ui/button";

  let { token, onDone }: { name: string; token: string; onDone: () => void } =
    $props();

  const shortcutUrl = `${location.origin}/?token=${token}`;
</script>

<div class="flex min-h-svh items-center justify-center bg-muted p-4">
  <Card.Root class="w-full max-w-sm">
    <Card.Header>
      <Card.Title>Add to Home Screen</Card.Title>
      <Card.Description>
        Save a shortcut so this kiosk opens directly next time.
      </Card.Description>
    </Card.Header>

    <Card.Content class="flex flex-col gap-4">
      <ol class="flex flex-col gap-2 text-sm">
        <li>1. Tap the <strong>Share</strong> button in Safari's toolbar</li>
        <li>2. Scroll down and tap <strong>Add to Home Screen</strong></li>
        <li>3. Tap <strong>Add</strong> to confirm</li>
      </ol>
      <p
        class="break-all rounded bg-background px-3 py-2 font-mono text-xs text-muted-foreground"
      >
        {shortcutUrl}
      </p>
    </Card.Content>

    <Card.Footer>
      <Button onclick={onDone} class="w-full">Done</Button>
    </Card.Footer>
  </Card.Root>
</div>
