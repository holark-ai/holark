import { monaco } from './monaco'

let jsonHighlightingReady: Promise<unknown> | undefined

export async function colorizeSource(content: string, language: string) {
  if (language === 'json') {
    jsonHighlightingReady ??= (async () => {
      // JSON registers its tokenizer on first model use, unlike basic languages.
      // An empty, detached model activates it without creating an editor UI.
      const model = monaco.editor.createModel('', 'json')
      model.dispose()
      // Wait for the asynchronous JSON mode registration before colorizing.
      await monaco.json.getWorker()
    })()
    await jsonHighlightingReady
  }
  return monaco.editor.colorize(content, language, { tabSize: 4 })
}
