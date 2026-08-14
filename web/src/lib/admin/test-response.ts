export function response(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
  });
}

/**
 * Three-segment base64url JWT with the given expiry, satisfying the session
 * freshness check. `expiresAt` is milliseconds since the epoch; the encoded
 * `exp` claim is `expiresAt / 1_000`. Defaults to one hour from now.
 */
export function testJwt(expiresAt = Date.now() + 3_600_000): string {
  const segment = (value: unknown): string =>
    btoa(JSON.stringify(value))
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  return [
    segment({ alg: "none", typ: "JWT" }),
    segment({ exp: expiresAt / 1_000 }),
    "signature",
  ].join(".");
}
