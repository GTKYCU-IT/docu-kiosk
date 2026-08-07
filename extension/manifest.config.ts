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
    default_popup: 'src/intercepted/index.html',
  },

  permissions: [
    'declarativeNetRequest',
    'storage',
  ],

  host_permissions: [
    'https://*.docusign.net/*',
    'https://*.docusign.com/*',
    'https://*.local/*',
    ...(config.mode === 'development' ? ['http://localhost/*'] : []),
  ],

  background: {
    "service_worker": "src/background.ts"
  },

  options_page: 'src/intercepted/index.html',

  web_accessible_resources: [{
    resources: [
      'src/intercepted/index.html',
      'assets/*',
      'public/*',
    ],
    matches: ['<all_urls>'],
  }],

}))
