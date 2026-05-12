<script lang="ts">
  import * as Card from "$lib/components/ui/card";
</script>

<div class="flex min-h-svh items-center justify-center bg-muted p-4">
  <Card.Root class="w-full max-w-sm">
    <Card.Header>
      <Card.Title>Add to Home Screen</Card.Title>
      <Card.Description>
        This kiosk must be installed as an app before use.
      </Card.Description>
    </Card.Header>

    <Card.Content>
      <ol class="flex flex-col gap-2 text-sm">
        <li>1. Tap the <strong>Share</strong> button in Safari's toolbar</li>
        <li>2. Scroll down and tap <strong>Add to Home Screen</strong></li>
        <li>3. Tap <strong>Add</strong> to confirm</li>
        <li>4. Open the app from your Home Screen to continue</li>
      </ol>
    </Card.Content>
  </Card.Root>
</div>
