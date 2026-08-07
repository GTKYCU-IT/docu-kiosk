import { defineManifest } from '@crxjs/vite-plugin'
import pkg from './package.json'

export default defineManifest((config) => ({
  manifest_version: 3,
  name: pkg.name,
  version: pkg.version,

  icons: {
    48: 'public/logo.png'
  },

  action: {
    default_icon: {
      48: 'public/logo.png'
    },
  },

  permissions: [
    'declarativeNetRequest',
    'storage',
    'tabs',
    'webNavigation',
  ],

  host_permissions: [
    // docusign.net covers the signing pods (demo, na2, www, ...); apps.docusign.com
    // is the new embedded-signing host. app.docusign.com (manage/send UI) stays
    // outside the extension's reach so staff can use DocuSign normally.
    'https://*.docusign.net/*',
    'https://apps.docusign.com/*',
    'https://*.local/*',
    ...(config.mode === 'development' ? ['http://localhost/*'] : []),
  ],

  background: {
    "service_worker": "src/background.ts"
  },

  options_page: 'src/options/index.html',

  web_accessible_resources: [{
    resources: [
      'src/intercepted/index.html',
      'assets/*',
      'public/*',
    ],
    matches: ['<all_urls>'],
  }],

}))
