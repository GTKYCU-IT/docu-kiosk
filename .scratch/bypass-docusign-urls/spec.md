## Problem Statement

Staff using the DocuKiosk extension sometimes need to access *completed* documents in DocuSign — documents that no longer require signing. Because completed documents share the same URL structure as active signing sessions (`/Signing/…`, `/signing/…`, `/Member/PowerForm…`, `/authenticate…`), the extension intercepts them and routes them to the kiosk picker. There is currently no way to open the intercepted URL directly in the browser without copying it from the details block and pasting it into a new tab — a friction that becomes painful when reviewing multiple completed documents.

## Solution

Add a **bypass** button to the intercepted page that opens the intercepted URL in a new browser tab, bypassing the extension interception net. The new tab is protected from re-interception for its entire lifetime (including DocuSign redirect chains and subsequent navigation within the tab), so the user can browse DocuSign normally until they close the tab. If the bypass cannot be performed (e.g., rule quota exhausted), the URL is copied to the clipboard and a toast message informs the user.

## User Stories

1. As a staff member reviewing completed DocuSign documents, I want to open an intercepted URL directly in a browser tab, so that I can view the completed document instead of being forced through the kiosk flow.
2. As a staff member who accidentally clicked a DocuSign link that does not need signature, I want an escape hatch from the kiosk picker, so that I am not stuck on the intercepted page with no way out.
3. As a staff member, I want the bypassed tab to survive DocuSign redirects (e.g., authentication redirects, session token refresh redirects), so that the document loads without being re-intercepted mid-chain.
4. As a staff member navigating around DocuSign after a bypass, I want to continue browsing (viewing other envelopes, checking statuses) within the same tab without re-interception, so that the bypass feels like a normal browser session.
5. As a staff member who opened a bypass tab, I want the intercepted page to stay open behind it, so that I can still send the URL to a kiosk if I realize the document actually needs signing.
6. As a staff member working when no kiosks are connected, I want the bypass button to still be available, so that I can access completed documents without waiting for a kiosk to come online.
7. As a staff member, I want the bypass button to be discoverable but visually secondary to the kiosk routing buttons, so that the primary signing workflow remains dominant.
8. As a staff member whose bypass attempt fails for technical reasons, I want the URL copied to my clipboard automatically, so that I can still open it manually.

## Implementation Decisions

### Domain terminology

- **Bypass** — the action of opening an intercepted DocuSign URL directly in a browser tab instead of routing it to a signing kiosk.
- **Intercept rules** — the four `declarativeNetRequest` rules (IDs 1–4) that redirect DocuSign signing URLs to the intercepted page.
- **Bypass rule** — a per-tab, tab-scoped `allow` DNR rule (IDs 100+) that punches a hole through the intercept net for the lifetime of a single bypass tab.

### Overall mechanism

When the user clicks the bypass button on the intercepted page, `App.svelte` sends a message (`{ type: "bypass", url }`) to the service worker via `chrome.runtime.sendMessage`. The service worker:

1. Creates a blank tab (`about:blank`) to obtain a stable `tabId`.
2. Installs a tab-scoped `allow` DNR rule (`action: "allow"`, `priority: 100`, `condition.tabIds: [tabId]`) with a dynamically allocated rule ID starting at 100.
3. Navigates the blank tab to the intercepted URL via `chrome.tabs.update(tabId, { url })`.

The allow rule matches DocuSign hosts (`*.docusign.net` and `*.docusign.com`), has priority 100 (above the intercept rules at priority 1), and is scoped to the single tab via `condition.tabIds`. Because `tabId` is stable across redirects and subsequent same-tab navigations, the entire DocuSign session within that tab is protected from re-interception.

### Tab-scoped allow rule

A bypass rule is a `declarativeNetRequest` dynamic rule with:
- `id`: allocated dynamically starting at 100 (one per active bypass tab)
- `priority`: 100 (higher than the intercept rules at priority 1)
- `action.type`: `allow`
- `condition`: `urlFilter: "*://*.docusign.net/*"` and `"*://*.docusign.com/*"`, `resourceTypes: ["main_frame"]`, `tabIds: [tabId]`

### Rule lifecycle

- **Creation**: when the bypass message is received, before navigating the tab.
- **Cleanup**: when the bypass tab closes (`chrome.tabs.onRemoved`), the corresponding rule is removed via `updateDynamicRules({ removeRuleIds: [id] })`.
- **Multi-tab**: each bypass tab gets its own rule ID and its own `onRemoved` listener. Two simultaneous bypass tabs do not collide.
- **Startup sweep**: `installRules()` removes all rule IDs >= 100 at service worker startup, cleaning up any stale rules from a browser crash or forced shutdown where `onRemoved` did not fire.

### Message contract between intercepted page and service worker

The intercepted page sends:

```json
{ "type": "bypass", "url": "<the intercepted DocuSign URL>" }
```

The service worker listens via `chrome.runtime.onMessage.addListener`. It does not send a response — the new tab opening is its own feedback.

### Bypass button in the intercepted page

- **Label**: "Open in browser"
- **Icon**: `ExternalLink` from `@lucide/svelte`
- **Variant**: ghost/secondary, placed below the kiosk list (after the `{#each}` block for kiosks, before the URL `<details>` block)
- **Visibility**: always rendered when a `pendingUrl` is present, regardless of whether kiosks are connected or not
- **Loading state**: none — the browser opening a new tab is the feedback
- **Failure state**: if the service worker fails to perform the bypass, a toast reads "Could not open in browser. The URL has been copied to your clipboard." and the existing `copyPendingUrl()` function copies the URL.

### Rule ID allocation

- Rules 1–4: intercept patterns (existing, unchanged)
- Rules 5–99: reserved for future intercept patterns
- Rules 100+: bypass allow rules, allocated incrementally

## Testing Decisions

### What makes a good test

Tests assert observable external behavior — rule installation, message handling, tab lifecycle — not internal state or implementation details. Tests use Vitest with Chrome API mocks (`vi.stubGlobal`), following the existing `background.wiring.test.ts` pattern.

### Primary seam: background wiring test

Extensions to `background.wiring.test.ts`:

1. **Bypass message handler**: stub `chrome.runtime.onMessage.addListener`, send a `{ type: "bypass", url: "https://demo.docusign.net/Signing/…" }` message, assert:
   - `chrome.tabs.create` is called (blank tab)
   - `chrome.declarativeNetRequest.updateDynamicRules` is called with a rule containing `action.type: "allow"`, `priority: 100`, `condition.tabIds` matching the created tab
   - `chrome.tabs.update` navigates the tab to the intercepted URL

2. **Tab close cleanup**: simulate a bypass tab being removed (`chrome.tabs.onRemoved.addListener`), assert the corresponding rule is removed from dynamic rules.

3. **Startup sweep**: on `installRules()`, assert that rule IDs >= 100 are included in `removeRuleIds` alongside the existing `ALL_RULE_IDS` sweep.

4. **Multi-tab**: create two bypass tabs, close one, assert only the closed tab rule is removed and the other remains active.

### Secondary seam: background logic test

Extensions to `background.test.ts`:

1. **Bypass rule construction**: if a `buildBypassRule(tabId)` helper is extracted, test that the returned rule has the correct `action`, `priority`, and `condition.tabIds`.

### Prior art

- `background.wiring.test.ts` lines 1–38: `vi.stubGlobal("chrome", …)` mock pattern, reused verbatim.
- `background.wiring.test.ts` lines 52–82: rule installation tests asserting `updateDynamicRules` calls, pattern reused for bypass rule installation and cleanup.
- `background.wiring.test.ts` lines 84–90: action click listener test, pattern reused for `onMessage` listener test.
- `background.test.ts` lines 85–106: `buildRules` describes rule shape tests, pattern reused for `buildBypassRule`.

## Out of Scope

- A persistent/pre-flight toggle to disable interception globally (the bypass is strictly one-shot per click).
- Modifying the DocuSign URL patterns (the intercept rules remain 1–4).
- Testing the `App.svelte` button at the component level — the message contract is tested transitively through the service worker handler.
- A "send to kiosk from the bypassed tab" re-interception workflow.
- Any server-side changes (broker, hub, database).

## Further Notes

- The `tabs` permission is not required additionally — `chrome.tabs.create` is already used in the toolbar action handler.
- The `ExternalLink` icon is already available via `@lucide/svelte` (no new dependency).
- The `copyPendingUrl()` function already exists in `App.svelte` — reused for the failure fallback.
- If DocuSign adds a 5th signing URL pattern, it would use rule ID 5, and the bypass sweep (IDs 100+) remains unaffected.
