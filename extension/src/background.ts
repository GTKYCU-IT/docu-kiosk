if (typeof globalThis.chrome !== 'undefined') {
  chrome.declarativeNetRequest.updateDynamicRules({
    removeRuleIds: [1],
    addRules: [{
      id: 1,
      priority: 1,
      action: {
        type: 'redirect' as chrome.declarativeNetRequest.RuleActionType,
        redirect: {
          regexSubstitution: `${chrome.runtime.getURL('src/intercepted/index.html')}#url=\\0`
        }
      },
      condition: {
        regexFilter: '^https://[^/]*\\.docusign\\.(net|com)/.*',
        resourceTypes: ['main_frame' as chrome.declarativeNetRequest.ResourceType]
      }
    }]
  })
}