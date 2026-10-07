export const maxCommentCharacters = 65536
export function quoteComment(body: string) {
  return body.replace(/\r\n?/g, '\n').split('\n').map((line) => `> ${line}`).join('\n') + '\n\n'
}
export function validBody(body: string) {
  return body.trim().length > 0 && Array.from(body).length <= maxCommentCharacters
}
