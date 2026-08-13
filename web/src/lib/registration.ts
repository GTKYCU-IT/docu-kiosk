// Client-side classification of the kiosk registration response. The server
// answers 204 No Content on success and an RFC 9457 application/problem+json
// document on failure; the problem `type` field is the discriminator this
// module keys on. The module is DOM-free (no fetch, no window, no location),
// so the classifier runs and tests under any runtime.

export type RegistrationOutcome =
  | { kind: "registered" } // 204: identity established; reload into the kiosk session
  | { kind: "already-registered" } // this kiosk is already registered; reopen the session
  | { kind: "name-conflict" } // the name is held by another kiosk; keep the form
  | { kind: "rejected" }; // any other response; safe generic handling

// Registration problem type URIs, mirrored from internal/server (RFC 9457
// `type`). These exact strings are the stable client contract.
export const PROBLEM_ALREADY_REGISTERED = "urn:docu-kiosk:problem:kiosk-already-registered";
export const PROBLEM_NAME_CONFLICT = "urn:docu-kiosk:problem:kiosk-name-conflict";

const PROBLEM_MEDIA_TYPE = "application/problem+json";

function isProblemJson(contentType: string | null | undefined): boolean {
  if (!contentType) return false;
  const [mediaType] = contentType.split(";");
  return mediaType.trim().toLowerCase() === PROBLEM_MEDIA_TYPE;
}

/**
 * Classify a registration response into the outcome the UI should act on.
 *
 * A 204 is a successful registration. Any other response is an RFC 9457
 * problem document only when its Content-Type is application/problem+json;
 * within such a document the exact `type` URI decides already-registered
 * versus name-conflict. Malformed bodies, non-problem responses, and other
 * problem types (invalid-kiosk-name, internal-error, ...) classify as
 * "rejected" so callers fall back to generic failure handling.
 */
export function classifyRegistration(
  status: number,
  contentType: string | null,
  body: string,
): RegistrationOutcome {
  if (status === 204) return { kind: "registered" };
  if (!isProblemJson(contentType)) return { kind: "rejected" };
  let doc: unknown;
  try {
    doc = JSON.parse(body);
  } catch {
    return { kind: "rejected" };
  }
  if (typeof doc !== "object" || doc === null || Array.isArray(doc)) {
    return { kind: "rejected" };
  }
  const type = (doc as Record<string, unknown>).type;
  if (typeof type !== "string") return { kind: "rejected" };
  switch (type) {
    case PROBLEM_ALREADY_REGISTERED:
      return { kind: "already-registered" };
    case PROBLEM_NAME_CONFLICT:
      return { kind: "name-conflict" };
    default:
      return { kind: "rejected" };
  }
}
