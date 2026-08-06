import { describe, it, expect } from 'vitest'
import { SIGNING_URL_FILTERS, buildRules, buildBypassRules } from './background'

// A URL matches the interception rules if any of the short per-pattern rules match.
const matchAnySigningFilter = (url: string) =>
  SIGNING_URL_FILTERS.some((f) => new RegExp(f).test(url))

describe('SIGNING_URL_FILTERS', () => {
  it('matches legacy docusign.net signing URLs (classic embedded signing)', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/Signing/StartInSession.aspx?t=abc123')).toBe(true)
    expect(matchAnySigningFilter('https://na2.docusign.net/Signing/SessionInitiate.aspx?t=abc123')).toBe(true)
  })

  it('matches current email signing links (MTRedeem) on www.docusign.net', () => {
    expect(matchAnySigningFilter('https://www.docusign.net/Signing/MTRedeem/v1/5a948afa-34a9-441b-8919-4033ee57d46c/na?slt=eyJ0eXAiOiJNVCJ9.long-signed-token')).toBe(true)
  })

  it('matches signing URLs with very long tokens (up to 4000 chars per DocuSign)', () => {
    const longToken = 'x'.repeat(3000)
    expect(matchAnySigningFilter(`https://www.docusign.net/Signing/MTRedeem/v1/abc/na?slt=${longToken}`)).toBe(true)
  })

  it('matches StartInSession signing links with a code= parameter', () => {
    expect(matchAnySigningFilter('https://www.docusign.net/Signing/StartInSession.aspx?code=eyJ0...&persistent_auth_token=no_client_token')).toBe(true)
  })

  it('matches lowercase /signing/ paths', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/signing/session/abc123')).toBe(true)
  })

  it('matches PowerForm signing URLs', () => {
    expect(matchAnySigningFilter('https://demo.docusign.net/Member/PowerFormSigning.aspx?PowerFormId=abc-123&env=na2')).toBe(true)
  })

  it('matches the new apps.docusign.com/authenticate signing host', () => {
    expect(matchAnySigningFilter('https://apps.docusign.com/authenticate?token=eyJ0eXAiOiJNVCJ9.very-long-token-up-to-4000-chars')).toBe(true)
    expect(matchAnySigningFilter('https://apps.docusign.com/authenticate/abc123')).toBe(true)
  })

  it('does NOT match the staff-facing DocuSign web app', () => {
    for (const url of [
      'https://app.docusign.com/home',
      'https://app.docusign.com/documents?view=sent',
      'https://account.docusign.com/login',
      'https://www.docusign.com/',
      'https://support.docusign.com/s/article/help',
      'https://docusign.com/',
    ]) {
      expect(matchAnySigningFilter(url)).toBe(false)
    }
  })

  it('does NOT match non-signing paths on signing hosts', () => {
    for (const url of [
      'https://apps.docusign.com/',
      'https://apps.docusign.com/documents',
      'https://demo.docusign.net/Member/MemberLogin.aspx',
      'https://demo.docusign.net/webFile.aspx?docId=123',
      'https://demo.docusign.net/AuthorizeWithMFA.aspx',
    ]) {
      expect(matchAnySigningFilter(url)).toBe(false)
    }
  })

  it('does NOT match non-DocuSign hosts', () => {
    expect(matchAnySigningFilter('https://broker.internal/api/kiosks')).toBe(false)
    expect(matchAnySigningFilter('https://example.com/Signing/StartInSession.aspx')).toBe(false)
  })
})

describe('RE2 compatibility (Chrome DNR regexFilter syntax)', () => {
  // Chrome's declarativeNetRequest regexFilter follows RE2 syntax, which
  // lacks lookarounds, atomic groups, and backreferences. Keep it that way.
  const forbidden = ['(?=', '(?!', '(?<=', '(?<!', '(?>', '\\1', '\\2', '\\b']

  it('avoids RE2-unsupported constructs', () => {
    for (const pattern of SIGNING_URL_FILTERS) {
      for (const token of forbidden) {
        expect(pattern, `pattern should not contain ${token}`).not.toContain(token)
      }
    }
  })
})

describe('buildRules', () => {
  const base = 'chrome-extension://abc123/src/intercepted/index.html'

  it('keeps every regex well under Chrome\'s 2KB compiled-regex rule limit', () => {
    // Chrome rejects regexFilters whose compiled program exceeds ~2KB (a single
    // multi-alternation regex was ~5.7KB and silently never installed). RE2
    // program size tracks pattern length; 80 chars keeps each pattern near the
    // ~35-43 instruction size of the filter that shipped in production.
    for (const f of SIGNING_URL_FILTERS) {
      expect(f.length).toBeLessThanOrEqual(80)
    }
  })

  it('creates one short regex rule per signing pattern', () => {
    const rules = buildRules(base)
    expect(rules.map((r) => r.id)).toEqual([1, 2, 3, 4])
    expect(rules.map((r) => r.condition.regexFilter)).toEqual(SIGNING_URL_FILTERS)
    expect(rules.every((r) => r.condition.initiatorDomains === undefined)).toBe(true)
    expect(rules.every((r) => r.action.type === 'redirect')).toBe(true)
    expect(rules.every((r) => String(r.action.redirect?.regexSubstitution).includes('#url=\\0'))).toBe(true)
  })
})

describe('buildBypassRules', () => {
  it('creates two allow rules for a bypass tab, one per DocuSign host', () => {
    const rules = buildBypassRules(42, 100)
    expect(rules).toHaveLength(2)
    expect(rules.map((r) => r.id)).toEqual([100, 101])
    expect(rules.every((r) => r.priority === 100)).toBe(true)
    expect(rules.every((r) => r.action.type === 'allow')).toBe(true)
    expect(rules.every((r) => r.condition.resourceTypes?.includes('main_frame' as chrome.declarativeNetRequest.ResourceType))).toBe(true)
    expect(rules.every((r) => r.condition.tabIds?.includes(42))).toBe(true)
  })

  it('scopes each rule to the provided tabId', () => {
    const rulesA = buildBypassRules(10, 200)
    const rulesB = buildBypassRules(20, 300)
    expect(rulesA[0].condition.tabIds).toEqual([10])
    expect(rulesB[0].condition.tabIds).toEqual([20])
  })

  it('matches docusign.net and docusign.com hosts via urlFilter', () => {
    const rules = buildBypassRules(1, 100)
    expect(rules[0].condition.urlFilter).toBe('*://*.docusign.net/*')
    expect(rules[1].condition.urlFilter).toBe('*://*.docusign.com/*')
  })

  it('produces allow rules that out-prioritise the intercept redirect rules', () => {
    const bypass = buildBypassRules(1, 100)
    const intercept = buildRules('chrome-extension://x/src/intercepted/index.html')
    for (const br of bypass) {
      for (const ir of intercept) {
        expect(br.priority!).toBeGreaterThan(ir.priority!)
      }
    }
  })
})

