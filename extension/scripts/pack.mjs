import { execFileSync, execSync } from 'child_process'
import { existsSync, renameSync, readFileSync, writeFileSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const { version } = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))

const EXTENSION_ID = 'ndmpfjhihnpgakamhhdcpjemakdgmkcp'
const CODEBASE_URL = 'http://192.168.168.77:8000/docu-kiosk.crx'

const edgeCandidates = [
  '/mnt/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  '/mnt/c/Program Files/Microsoft/Edge/Application/msedge.exe',
]
const edgeBin = edgeCandidates.find(p => existsSync(p)) ?? 'msedge'

const toWin = p => execSync(`wslpath -w "${p}"`).toString().trim()

execFileSync(edgeBin, [
  `--pack-extension=${toWin(join(root, 'dist'))}`,
  `--pack-extension-key=${toWin(join(root, 'dist.pem'))}`,
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
