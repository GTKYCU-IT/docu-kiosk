<script lang="ts">
  import * as Card from "$lib/components/ui/card";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Field, FieldGroup, FieldLabel } from "$lib/components/ui/field";
  import { toast } from "svelte-sonner";
  import { classifyRegistration } from "$lib/registration";

  let {
    onRegistered,
    onAlreadyRegistered,
  }: { onRegistered: () => void; onAlreadyRegistered: () => void } = $props();

  let name = $state("");
  let loading = $state(false);

  async function register(e: SubmitEvent) {
    e.preventDefault();
    if (loading) return;
    loading = true;
    try {
      const res = await fetch("/api/kiosks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      });
      const outcome = classifyRegistration(
        res.status,
        res.headers.get("content-type"),
        await res.text(),
      );
      switch (outcome.kind) {
        case "registered":
          // New identity established: reload into the kiosk session.
          onRegistered();
          return;
        case "already-registered":
          // The kiosk identity exists but the original 204 was lost. The App
          // reopens the broker session; the greeting supplies the name.
          onAlreadyRegistered();
          return;
        case "name-conflict":
          // The name is held by another kiosk. Keep the submitted input on
          // the form and leave registration unresolved.
          toast.error("That name is already in use by another kiosk. Choose a different name.");
          return;
        case "rejected":
          toast.error("Registration failed. Please try again.");
          return;
      }
    } catch {
      toast.error("Could not reach the server. Check your network connection.");
    } finally {
      loading = false;
    }
  }

  const id = $props.id();
</script>

<div class="flex min-h-svh items-center justify-center bg-muted p-4">
  <Card.Root class="w-full max-w-sm">
    <Card.Header>
      <Card.Title class="text-2xl">Register Kiosk</Card.Title>
      <Card.Description>Give this kiosk a name to identify it.</Card.Description
      >
    </Card.Header>

    <Card.Content>
      <form onsubmit={register}>
        <FieldGroup>
          <Field>
            <FieldLabel for="name-{id}">Kiosk Name</FieldLabel>
            <Input
              id="name-{id}"
              bind:value={name}
              placeholder="e.g. Branch Office Kiosk 1"
              required
            />
          </Field>

          <Field>
            <Button type="submit" class="w-full" disabled={loading}>
              {loading ? "Registering…" : "Register"}
            </Button>
          </Field>
        </FieldGroup>
      </form>
    </Card.Content>
  </Card.Root>
</div>
