// hermes-safe-update settings migrator.
//
// Runs on Hermes' own bundled Electron so Chromium reads and writes its own
// LevelDB format (never hand-edit LevelDB). The Go side copies the desktop
// app's "Local Storage" into a private work directory and points this script
// at that COPY; it is never run against the live userData.
//
// The Go side decides everything (which origins, which keys, merged values);
// this script only reads and writes localStorage through the real API.
//
//   electron main.js --user-data=<dir> --out=<dir> --nonce=<n> --mode=dump
//                    --origins=<origin>,<origin>,...
//   electron main.js --user-data=<dir> --out=<dir> --nonce=<n> --mode=write
//                    --origin=<origin> --in=<writes.json>
//
// dump  writes <out>/dump.json  {"nonce", "origins": {origin: {key: value}}}
// write sets every key of <writes.json> ({key: value}) under one origin,
//       flushes, re-reads in the same process and writes
//       <out>/write.json {"nonce", "written", "mismatches": [key names]}.
// Exit codes: 0 ok, 1 error or verify mismatch, 2 bad usage, 3 watchdog.
// The output files hold values (they can contain session ids): the Go side
// deletes the work directory when it is done. stdout carries counts only.
'use strict'

// FIRST: Electron's default handler for an uncaught error in main shows a
// native modal error dialog and blocks. Never let that happen; print and exit.
process.on('uncaughtException', e => {
  console.error('migrator: ' + ((e && e.stack) || e))
  process.exit(2)
})
process.on('unhandledRejection', e => {
  console.error('migrator: ' + ((e && e.stack) || e))
  process.exit(2)
})

const childProcess = require('child_process')
const fs = require('fs')
const path = require('path')

// ELECTRON_RUN_AS_NODE (even set to "") turns Electron into plain Node and
// require('electron') no longer gives the app API. If we inherited it,
// relaunch ourselves once without it and pass the exit code through.
if (!process.versions.electron || process.type !== 'browser') {
  if (process.env.HSU_MIGRATOR_RELAUNCHED) {
    console.error('migrator: still in node mode after relaunch')
    process.exit(1)
  }
  const env = Object.assign({}, process.env)
  delete env.ELECTRON_RUN_AS_NODE
  env.HSU_MIGRATOR_RELAUNCHED = '1'
  const r = childProcess.spawnSync(process.execPath, process.argv.slice(1), { env, stdio: 'inherit', windowsHide: true })
  process.exit(r.status === null ? 1 : r.status)
}

const { app, BrowserWindow, session, dialog } = require('electron')
// Belt and braces: no error box, ever.
if (dialog) dialog.showErrorBox = () => {}

function arg(name, dflt) {
  const hit = process.argv.find(a => a.startsWith(`--${name}=`))
  return hit ? hit.slice(name.length + 3) : dflt
}

const rawUserData = arg('user-data')
const rawOut = arg('out')
const nonce = arg('nonce', '')
const mode = arg('mode')
const watchdogMs = Number(arg('watchdog-ms', '110000'))

function usage(msg) {
  console.error('migrator: ' + msg)
  process.exit(2)
}
if (!rawUserData || !rawOut || !nonce) usage('need --user-data, --out and --nonce')
const userData = path.resolve(rawUserData)
const outDir = path.resolve(rawOut)
if (!path.isAbsolute(rawUserData) || !path.isAbsolute(rawOut)) usage('--user-data and --out must be absolute paths')
if (mode !== 'dump' && mode !== 'write') usage('--mode must be dump or write')
if (!fs.existsSync(path.join(userData, 'Local Storage', 'leveldb'))) usage('no Local Storage/leveldb under --user-data')
fs.mkdirSync(outDir, { recursive: true })

// The Go side kills us on its own timeout, but a relaunched child would
// survive that; never outlive the deadline either way.
setTimeout(() => { console.error('migrator: watchdog expired'); app.exit(3) }, watchdogMs).unref()

app.setPath('userData', userData)
app.disableHardwareAcceleration()
app.commandLine.appendSwitch('disable-gpu')

const BLANK = '<!doctype html><title>m</title>'
const DUMP_JS = 'JSON.stringify(Object.fromEntries(Array.from({length: localStorage.length}, (_, i) => localStorage.key(i)).map(k => [k, localStorage.getItem(k)])))'

let blankFile = ''
function pageFor(origin) {
  if (origin === 'file://') return 'file:///' + blankFile.replace(/\\/g, '/')
  return origin + '/'
}

async function readOrigin(win, origin) {
  await win.loadURL(pageFor(origin))
  return JSON.parse(await win.webContents.executeJavaScript(DUMP_JS))
}

function writeJSON(name, obj) {
  const p = path.join(outDir, name)
  fs.writeFileSync(p + '.tmp', JSON.stringify(obj))
  fs.renameSync(p + '.tmp', p)
}

app.whenReady().then(async () => {
  let code = 0
  try {
    // Serve a blank page for every http origin from memory: no port is
    // bound, so this never collides with a running Hermes on that port.
    session.defaultSession.protocol.handle('http', () =>
      new Response(BLANK, { headers: { 'content-type': 'text/html' } })
    )
    blankFile = path.join(outDir, 'blank.html')
    fs.writeFileSync(blankFile, BLANK)
    const win = new BrowserWindow({ show: false, webPreferences: { contextIsolation: true, sandbox: true } })

    if (mode === 'dump') {
      const origins = (arg('origins', '') || '').split(',').filter(Boolean)
      const out = {}
      for (const o of origins) out[o] = await readOrigin(win, o)
      writeJSON('dump.json', { nonce, origins: out })
      console.log('dump ok: ' + origins.map(o => `${o}=${Object.keys(out[o]).length}`).join(' '))
    } else {
      const origin = arg('origin')
      const inFile = arg('in')
      if (!origin || !inFile) usage('write needs --origin and --in')
      const writes = JSON.parse(fs.readFileSync(inFile, 'utf8'))
      await win.loadURL(pageFor(origin))
      await win.webContents.executeJavaScript(
        `(() => { const w = ${JSON.stringify(writes)}; for (const k in w) localStorage.setItem(k, w[k]); return Object.keys(w).length })()`
      )
      await session.defaultSession.flushStorageData()
      await new Promise(r => setTimeout(r, 1500))
      const after = await readOrigin(win, origin)
      const bad = Object.keys(writes).filter(k => after[k] !== writes[k])
      writeJSON('write.json', { nonce, written: Object.keys(writes).length, mismatches: bad })
      console.log(`write ok: wrote ${Object.keys(writes).length} keys; verify mismatches: ${bad.length}`)
      if (bad.length) code = 1
    }
    win.destroy()
  } catch (e) {
    console.error('migrator: ERROR ' + ((e && e.message) || e))
    code = 1
  }
  try { await session.defaultSession.flushStorageData() } catch (e) { code = code || 1 }
  setTimeout(() => app.exit(code), 800)
})
