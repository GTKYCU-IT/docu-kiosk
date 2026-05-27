if (typeof globalThis.chrome !== 'undefined') {
  chrome.declarativeNetRequest.updateDynamicRules({
    removeRuleIds: [1, 2],
    addRules: [
      {
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
      },
      {
        id: 2,
        priority: 2,
        action: {
          type: 'block' as chrome.declarativeNetRequest.RuleActionType,
        },
        condition: {
          regexFilter: '^https://[^/]*\\.docusign\\.(net|com)/.*',
          resourceTypes: ['sub_frame' as chrome.declarativeNetRequest.ResourceType]
        }
      }
    ]
  })

  chrome.webNavigation.onBeforeNavigate.addListener(
    (details) => {
      if (details.frameId === 0) return
      chrome.tabs.create({
        url: `${chrome.runtime.getURL('src/intercepted/index.html')}#url=${details.url}`,
        active: true,
      })
    },
    {
      url: [
        { hostSuffix: '.docusign.net', schemes: ['https'] },
        { hostEquals: 'docusign.net', schemes: ['https'] },
        { hostSuffix: '.docusign.com', schemes: ['https'] },
        { hostEquals: 'docusign.com', schemes: ['https'] },
      ]
    }
  )
}