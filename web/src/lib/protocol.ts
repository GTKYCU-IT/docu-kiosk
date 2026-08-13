// Wire message shapes, hand-mirrored from the broker's protocol package
// (internal/hub). The message set is sealed and maintained by hand on both
// sides: any change must be edited in Go and here in lockstep (see
// docs/adr/0003-hand-maintained-wire-protocol.md). This module is the only
// TypeScript home for the wire message string literals and field-shape
// checks; callers dispatch on the typed Message union instead of inspecting
// raw payloads.

export type Message =
  | { type: "connected"; name: string }
  | { type: "sign"; url: string };

/**
 * Decode a raw WebSocket payload into a typed wire message.
 *
 * Throws an Error for invalid JSON, non-object payloads (primitives, null,
 * arrays), a missing or unknown `type`, or a malformed required field.
 * Extra object fields beyond the known shape are ignored.
 */
export function parse(raw: string): Message {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new Error("protocol: invalid JSON");
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("protocol: expected a JSON object");
  }
  const record = value as Record<string, unknown>;
  switch (record.type) {
    case "connected":
      if (typeof record.name !== "string") {
        throw new Error("protocol: connected message requires a string name");
      }
      return { type: "connected", name: record.name };
    case "sign":
      if (typeof record.url !== "string") {
        throw new Error("protocol: sign message requires a string url");
      }
      return { type: "sign", url: record.url };
    default:
      throw new Error("protocol: unknown message type: " + String(record.type));
  }
}
