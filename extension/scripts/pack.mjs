import { execFileSync } from 'child_process'
import { existsSync, renameSync, readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const { version } = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))

const EXTENSION_ID = 'ndmpfjhihnpgakamhhdcpjemakdgmkcp'
const brokerHost = process.env.BROKER_HOST
if (!brokerHost) {
  console.error('BROKER_HOST environment variable is required')
  process.exit(1)
}
const CODEBASE_URL = `https://${brokerHost}/extension/docu-kiosk.crx`

const chromeCandidates = [
  '/usr/bin/google-chrome',
  '/usr/bin/chromium-browser',
  '/usr/bin/chromium',
]
const chromeBin = chromeCandidates.find(p => existsSync(p)) ?? 'google-chrome'

execFileSync(chromeBin, [
  `--pack-extension=${join(root, 'dist')}`,
  `--pack-extension-key=${join(root, 'dist.pem')}`,
], { stdio: 'inherit' })

renameSync(join(root, 'dist.crx'), join(root, 'public', 'docu-kiosk.crx'))
console.log('Moved dist.crx → public/docu-kiosk.crx')

writeFileSync(join(root, 'public', 'update.xml'),
  `<?xml version='1.0' encoding='UTF-8'?>
<gupdate xmlns='http://www.google.com/update2/response' protocol='2.0'>
  <app appid='${EXTENSION_ID}'>
    <updatecheck
      codebase='${CODEBASE_URL}'
      version='${version}' />
  </app>
</gupdate>
`)
console.log(`Generated update.xml for version ${version}`)
