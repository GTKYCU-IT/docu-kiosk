<script lang="ts">
  import * as Card from "$lib/components/ui/card";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Field, FieldGroup, FieldLabel } from "$lib/components/ui/field";
  import { toast } from "svelte-sonner";

  let name = $state("");
  let key = $state("");
  let loading = $state(false);

  async function register(e: SubmitEvent) {
    e.preventDefault();
    loading = true;
    try {
      const res = await fetch("/api/kiosks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, key }),
      });
      if (!res.ok) {
        toast.error("Registration failed. Please try again.");
        return;
      }
      const { token } = await res.json();
      localStorage.setItem("kiosk-token", token);
      location.reload();
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
            <FieldLabel for="key-{id}">Secret Key</FieldLabel>
            <Input id="key-{id}" bind:value={key} required />
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
