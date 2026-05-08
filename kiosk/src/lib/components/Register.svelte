<script lang="ts">
  import * as Card from '$lib/components/ui/card'
  import { Button } from '$lib/components/ui/button'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'

  let kioskId = $state('')
  let secret = $state('')
  let error = $state('')
  let loading = $state(false)

  async function register(e: SubmitEvent) {
    e.preventDefault()
    error = ''
    loading = true
    try {
      const res = await fetch('/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kioskId, secret }),
      })
      if (!res.ok) {
        error = res.status === 401 ? 'Invalid secret.' : 'Registration failed.'
        return
      }
      const { token } = await res.json()
      localStorage.setItem('kioskId', kioskId)
      localStorage.setItem('kioskToken', token)
      location.reload()
    } catch {
      error = 'Could not reach the broker. Check your network connection.'
    } finally {
      loading = false
    }
  }
</script>

<div class="flex min-h-svh items-center justify-center bg-muted p-4">
  <Card.Root class="w-full max-w-sm">
    <Card.Header>
      <Card.Title>Register Kiosk</Card.Title>
      <Card.Description>
        Enter the kiosk ID and registration secret provided by your IT team.
      </Card.Description>
    </Card.Header>

    <form onsubmit={register}>
      <Card.Content class="flex flex-col gap-4">
        <div class="flex flex-col gap-1.5">
          <Label for="kioskId">Kiosk ID</Label>
          <Input
            id="kioskId"
            bind:value={kioskId}
            placeholder="e.g. office-101"
            required
          />
        </div>
        <div class="flex flex-col gap-1.5">
          <Label for="secret">Registration Secret</Label>
          <Input
            id="secret"
            type="password"
            bind:value={secret}
            required
          />
        </div>
        {#if error}
          <p class="text-sm text-destructive">{error}</p>
        {/if}
      </Card.Content>

      <Card.Footer>
        <Button type="submit" class="w-full" disabled={loading}>
          {loading ? 'Registering…' : 'Register'}
        </Button>
      </Card.Footer>
    </form>
  </Card.Root>
</div>
