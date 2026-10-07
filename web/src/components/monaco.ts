import * as monaco from 'monaco-editor'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import JSONWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'
import CSSWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker'
import HTMLWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker'
import TypeScriptWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker'

// Vite emits the editor worker with the application; no loader or CDN is used.
self.MonacoEnvironment = {
  getWorker: (_module, label) => {
    if (label === 'json') return new JSONWorker()
    if (['css', 'scss', 'less'].includes(label)) return new CSSWorker()
    if (['html', 'handlebars', 'razor'].includes(label)) return new HTMLWorker()
    if (['typescript', 'javascript'].includes(label)) return new TypeScriptWorker()
    return new EditorWorker()
  },
}

monaco.editor.defineTheme('holark-light', {
  base: 'vs', inherit: true,
  rules: [
    { token: '', foreground: '3C4148' },
    { token: 'keyword', foreground: '806798' },
    { token: 'string', foreground: '587960' },
    { token: 'comment', foreground: '9993A2' },
    { token: 'delimiter', foreground: '73707B' },
    { token: 'attribute.name', foreground: '806798' },
    { token: 'attribute.value', foreground: '587960' },
    { token: 'tag', foreground: '806798' },
    { token: 'number', foreground: '8A6E58' },
  ],
  colors: {
    'editor.background': '#FFFFFF', 'editor.foreground': '#3C4148',
    'editorLineNumber.foreground': '#A69FAE',
    'diffEditor.insertedLineBackground': '#ECF7ED',
    'diffEditor.removedLineBackground': '#FCF0F1',
    'diffEditor.insertedTextBackground': '#D7EEDB',
    'diffEditor.removedTextBackground': '#F7DDE0',
    'diffEditor.diagonalFill': '#FFFFFF',
    'diffEditor.border': '#EFECF2',
  },
})

export { monaco }
