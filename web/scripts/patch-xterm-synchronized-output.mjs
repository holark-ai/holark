import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const xtermRoot = path.join(webRoot, 'node_modules', '@xterm', 'xterm')
const expectedVersion = '6.0.0'

const manifest = JSON.parse(await readFile(path.join(xtermRoot, 'package.json'), 'utf8'))
if (manifest.version !== expectedVersion) {
  throw new Error(`expected @xterm/xterm ${expectedVersion}, found ${manifest.version ?? 'an unknown version'}`)
}

// xterm 6.0 predates the synchronous refresh plumbing used by the upstream
// fix. Carry that signal from ESU through CoreBrowserTerminal, then render
// immediately when the refresh is synchronous or flushes buffered rows.
// Also carry xterm.js PR #6011 until it ships upstream: top-anchored CSI S
// scrolls through BufferService so outgoing rows enter scrollback, while
// savedY follows ybase to preserve saved cursor restoration.
const bundlePatches = [
  {
    file: 'lib/xterm.mjs',
    replacements: [
      {
        before: 'refreshRows(e,i,r=!1){if(this._isPaused){this._needsFullRefresh=!0;return}if(this._coreService.decPrivateModes.synchronizedOutput){this._syncOutputHandler.bufferRows(e,i);return}let n=this._syncOutputHandler.flush();n&&(e=Math.min(e,n.start),i=Math.max(i,n.end)),r||(this._isNextRenderRedrawOnly=!1),this._renderDebouncer.refresh(e,i,this._rowCount)}',
        after: 'refreshRows(e,i,r=!1,o=!1){if(this._isPaused){this._needsFullRefresh=!0;return}if(this._coreService.decPrivateModes.synchronizedOutput){this._syncOutputHandler.bufferRows(e,i);return}let n=this._syncOutputHandler.flush();n&&(e=Math.min(e,n.start),i=Math.max(i,n.end)),r||(this._isNextRenderRedrawOnly=!1),o||n?this._renderRows(e,i):this._renderDebouncer.refresh(e,i,this._rowCount)}',
      },
      {
        before: 'this._inputHandler.onRequestRefreshRows(i=>this.refresh(i?.start??0,i?.end??this.rows-1))',
        after: 'this._inputHandler.onRequestRefreshRows(i=>this.refresh(i?.start??0,i?.end??this.rows-1,i?.sync??!1))',
      },
      {
        before: 'case 2026:this._coreService.decPrivateModes.synchronizedOutput=!1,this._onRequestRefreshRows.fire(void 0);break',
        after: 'case 2026:this._coreService.decPrivateModes.synchronizedOutput=!1,this._onRequestRefreshRows.fire({sync:!0});break',
      },
      {
        before: 'refresh(e,i){this._renderService?.refreshRows(e,i)}',
        after: 'refresh(e,i,r=!1){this._renderService?.refreshRows(e,i,!1,r)}',
      },
      {
        name: 'top-anchored scroll-up implementation',
        before: 'scrollUp(e){let i=e.params[0]||1;for(;i--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
        after: 'scrollUp(e){let i=e.params[0]||1;if(this._activeBuffer.scrollTop===0)for(;i--;){let e=this._activeBuffer.ybase;this._bufferService.scroll(this._eraseAttrData()),this._activeBuffer.savedY+=this._activeBuffer.ybase-e}else for(;i--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
      },
    ],
  },
  {
    file: 'lib/xterm.js',
    replacements: [
      {
        before: 'refreshRows(e,t,i=!1){if(this._isPaused)return void(this._needsFullRefresh=!0);if(this._coreService.decPrivateModes.synchronizedOutput)return void this._syncOutputHandler.bufferRows(e,t);const s=this._syncOutputHandler.flush();s&&(e=Math.min(e,s.start),t=Math.max(t,s.end)),i||(this._isNextRenderRedrawOnly=!1),this._renderDebouncer.refresh(e,t,this._rowCount)}',
        after: 'refreshRows(e,t,i=!1,r=!1){if(this._isPaused)return void(this._needsFullRefresh=!0);if(this._coreService.decPrivateModes.synchronizedOutput)return void this._syncOutputHandler.bufferRows(e,t);const s=this._syncOutputHandler.flush();s&&(e=Math.min(e,s.start),t=Math.max(t,s.end)),i||(this._isNextRenderRedrawOnly=!1),r||s?this._renderRows(e,t):this._renderDebouncer.refresh(e,t,this._rowCount)}',
      },
      {
        before: 'this._inputHandler.onRequestRefreshRows((e=>this.refresh(e?.start??0,e?.end??this.rows-1)))',
        after: 'this._inputHandler.onRequestRefreshRows((e=>this.refresh(e?.start??0,e?.end??this.rows-1,e?.sync??!1)))',
      },
      {
        before: 'case 2026:this._coreService.decPrivateModes.synchronizedOutput=!1,this._onRequestRefreshRows.fire(void 0)}',
        after: 'case 2026:this._coreService.decPrivateModes.synchronizedOutput=!1,this._onRequestRefreshRows.fire({sync:!0})}',
      },
      {
        before: 'refresh(e,t){this._renderService?.refreshRows(e,t)}',
        after: 'refresh(e,t,i=!1){this._renderService?.refreshRows(e,t,!1,i)}',
      },
      {
        name: 'top-anchored scroll-up implementation',
        before: 'scrollUp(e){let t=e.params[0]||1;for(;t--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
        after: 'scrollUp(e){let t=e.params[0]||1;if(0===this._activeBuffer.scrollTop)for(;t--;){const e=this._activeBuffer.ybase;this._bufferService.scroll(this._eraseAttrData()),this._activeBuffer.savedY+=this._activeBuffer.ybase-e}else for(;t--;)this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollTop,1),this._activeBuffer.lines.splice(this._activeBuffer.ybase+this._activeBuffer.scrollBottom,0,this._activeBuffer.getBlankLine(this._eraseAttrData()));return this._dirtyRowTracker.markRangeDirty(this._activeBuffer.scrollTop,this._activeBuffer.scrollBottom),!0}',
      },
    ],
  },
]

for (const bundlePatch of bundlePatches) {
  const bundlePath = path.join(xtermRoot, bundlePatch.file)
  let source = await readFile(bundlePath, 'utf8')
  let changed = false

  for (const replacement of bundlePatch.replacements) {
    const beforeCount = occurrences(source, replacement.before)
    const afterCount = occurrences(source, replacement.after)

    if (beforeCount === 1 && afterCount === 0) {
      source = source.replace(replacement.before, replacement.after)
      changed = true
      continue
    }
    if (beforeCount === 0 && afterCount === 1) continue

    throw new Error(
      `cannot patch @xterm/xterm ${expectedVersion} ${bundlePatch.file}: ` +
      `expected one unpatched or patched ${replacement.name ?? 'synchronized-output implementation'}, ` +
      `found ${beforeCount} unpatched and ${afterCount} patched`,
    )
  }

  if (changed) await writeFile(bundlePath, source)
}

function occurrences(source, value) {
  return source.split(value).length - 1
}
