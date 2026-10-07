import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const workerRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const xtermRoot = path.join(workerRoot, 'node_modules', '@xterm', 'headless')
const expectedVersion = '6.0.0'

const manifest = JSON.parse(await readFile(path.join(xtermRoot, 'package.json'), 'utf8'))
if (manifest.version !== expectedVersion) {
  throw new Error(`expected @xterm/headless ${expectedVersion}, found ${manifest.version ?? 'an unknown version'}`)
}

// Carry xterm.js PR #6011 until it ships upstream. CSI S should use the same
// scrollback-preserving path as LF only when the scroll region begins at row 1.
// savedY follows ybase so saved cursor restoration stays anchored to the same
// screen row.
const bundlePatches = [
  {
    file: 'lib-headless/xterm-headless.mjs',
    before: 'scrollUp(e){let r=e.params[0]||1;for(;r--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
    after: 'scrollUp(e){let r=e.params[0]||1;if(this._activeBuffer.scrollTop===0)for(;r--;){let e=this._activeBuffer.ybase;this._bufferService.scroll(this._eraseAttrData()),this._activeBuffer.savedY+=this._activeBuffer.ybase-e}else for(;r--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
  },
  {
    file: 'lib-headless/xterm-headless.js',
    before: 'scrollUp(e){let t=e.params[0]||1;for(;t--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
    after: 'scrollUp(e){let t=e.params[0]||1;if(0===this._activeBuffer.scrollTop)for(;t--;){const e=this._activeBuffer.ybase;this._bufferService.scroll(this._eraseAttrData()),this._activeBuffer.savedY+=this._activeBuffer.ybase-e}else for(;t--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
  },
]

for (const bundlePatch of bundlePatches) {
  const bundlePath = path.join(xtermRoot, bundlePatch.file)
  let source = await readFile(bundlePath, 'utf8')
  const beforeCount = occurrences(source, bundlePatch.before)
  const afterCount = occurrences(source, bundlePatch.after)

  if (beforeCount === 1 && afterCount === 0) {
    source = source.replace(bundlePatch.before, bundlePatch.after)
    await writeFile(bundlePath, source)
    continue
  }
  if (beforeCount === 0 && afterCount === 1) continue

  throw new Error(
    `cannot patch @xterm/headless ${expectedVersion} ${bundlePatch.file}: ` +
    `expected one unpatched or patched top-anchored scroll-up implementation, ` +
    `found ${beforeCount} unpatched and ${afterCount} patched`,
  )
}

function occurrences(source, value) {
  return source.split(value).length - 1
}
