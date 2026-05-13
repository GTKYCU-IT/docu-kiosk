import { describe, it, expect } from 'vitest'
import { captureSigningUrl } from './background'

type Details = Parameters<typeof captureSigningUrl>[0]

describe('captureSigningUrl', () => {
  it('returns the url unchanged for a GET request', () => {
    const details = { url: 'https://app.docusign.net/sign', method: 'GET' } as Details
    expect(captureSigningUrl(details)).toBe('https://app.docusign.net/sign')
  })

  it('appends form data as query params on POST', () => {
    const details: Details = {
      url: 'https://app.docusign.net/sign',
      method: 'POST',
      requestBody: { formData: { token: ['abc123'], env: ['prod'] } }
    } as unknown as Details
    const result = captureSigningUrl(details)
    expect(result).toContain('token=abc123')
    expect(result).toContain('env=prod')
  })

  it('returns url unchanged when POST has no body', () => {
    const details = { url: 'https://app.docusign.net/sign', method: 'POST' } as Details
    expect(captureSigningUrl(details)).toBe('https://app.docusign.net/sign')
  })
})
