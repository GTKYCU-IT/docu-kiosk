<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import * as Card from "$lib/components/ui/card";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    Field,
    FieldError,
    FieldGroup,
    FieldLabel,
  } from "$lib/components/ui/field";
  import {
    AdminSessionController,
    type AdminSessionState,
  } from "$lib/admin/session";

  let sessionState = $state<AdminSessionState>({ status: "restoring" });
  let username = $state("");
  let password = $state("");

  const session = new AdminSessionController({
    onChange: (nextState) => {
      sessionState = nextState;
    },
  });

  const id = $props.id();

  onMount(() => {
    void session.restore();
  });

  onDestroy(() => session.close());

  async function submitLogin(event: SubmitEvent) {
    event.preventDefault();
    await session.login(username, password);
    password = "";
  }
</script>

<main class="flex min-h-svh items-center justify-center bg-muted p-4">
  {#if sessionState.status === "restoring"}
    <p class="text-sm font-medium text-muted-foreground" role="status">
      Restoring administrator session
    </p>
  {:else if sessionState.status === "unavailable"}
    <Card.Root class="w-full max-w-sm">
      <Card.Header>
        <Card.Title>
          <h1 class="text-2xl font-semibold tracking-tight">Broker unavailable</h1>
        </Card.Title>
        <Card.Description>
          The administrator session could not be restored. Try again when the
          Broker is available.
        </Card.Description>
      </Card.Header>
      <Card.Footer>
        <Button class="w-full" onclick={() => session.retry()}>Retry</Button>
      </Card.Footer>
    </Card.Root>
  {:else if sessionState.status === "login" || sessionState.status === "invalid-credentials"}
    <Card.Root class="w-full max-w-sm">
      <Card.Header>
        <Card.Title>
          <h1 class="text-2xl font-semibold tracking-tight">Administrator sign in</h1>
        </Card.Title>
        <Card.Description>
          Enter your Broker administrator credentials for this browser profile.
        </Card.Description>
      </Card.Header>
      <Card.Content>
        <form onsubmit={submitLogin}>
          <FieldGroup>
            <Field>
              <FieldLabel for="admin-username-{id}">Username</FieldLabel>
              <Input
                id="admin-username-{id}"
                name="username"
                autocomplete="username"
                bind:value={username}
                aria-invalid={sessionState.status === "invalid-credentials"}
                required
              />
            </Field>
            <Field>
              <FieldLabel for="admin-password-{id}">Password</FieldLabel>
              <Input
                id="admin-password-{id}"
                name="password"
                type="password"
                autocomplete="current-password"
                bind:value={password}
                aria-invalid={sessionState.status === "invalid-credentials"}
                aria-describedby={sessionState.status === "invalid-credentials"
                  ? `admin-login-error-${id}`
                  : undefined}
                required
              />
              {#if sessionState.status === "invalid-credentials"}
                <FieldError id="admin-login-error-{id}">
                  Invalid username or password
                </FieldError>
              {/if}
            </Field>
            <Field>
              <Button class="w-full" type="submit" disabled={sessionState.submitting}>
                {sessionState.submitting ? "Signing in…" : "Sign in"}
              </Button>
            </Field>
          </FieldGroup>
        </form>
      </Card.Content>
    </Card.Root>
  {:else}
    <Card.Root class="w-full max-w-sm">
      <Card.Header>
        <Card.Title>
          <h1 class="text-2xl font-semibold tracking-tight">
            Administrator session active
          </h1>
        </Card.Title>
        <Card.Description>
          This administrator session is shared across tabs of this browser
          profile.
        </Card.Description>
      </Card.Header>
      {#if sessionState.status === "logout-failed"}
        <Card.Content>
          <p class="text-sm text-destructive" role="alert">
            Sign out failed. Your administrator session is still active.
          </p>
        </Card.Content>
      {/if}
      <Card.Footer>
        <Button
          class="w-full"
          variant="destructive"
          disabled={sessionState.status === "signing-out"}
          onclick={() => session.logout()}
        >
          {sessionState.status === "signing-out" ? "Signing out…" : "Sign out"}
        </Button>
      </Card.Footer>
    </Card.Root>
  {/if}
</main>
