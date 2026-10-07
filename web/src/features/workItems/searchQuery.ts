// Keep quoted values and label groups intact when controls edit other qualifiers.
export function searchTokens(raw: string): string[] {
  const tokens: string[] = []
  let start = 0, depth = 0, quoted = false, escaped = false
  for (let i = 0; i <= raw.length; i++) {
    const char = raw[i]
    if (i === raw.length || (!quoted && depth === 0 && /\s/.test(char))) {
      if (i > start) tokens.push(raw.slice(start, i))
      start = i + 1
      continue
    }
    if (quoted) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === '"') quoted = false
    } else if (char === '"') quoted = true
    else if (char === '(') depth++
    else if (char === ')') depth = Math.max(0, depth - 1)
  }
  return tokens
}

// The editor has one fixed model: any group may match; every label in that group is required.
export type LabelGroups = string[][]

export type LabelNode = { name: string } | { operator: 'AND' | 'OR'; children: LabelNode[] }

export function labelGroupsText(groups: LabelGroups): string {
  const parts = groups.filter((group) => group.length).map((group) => {
    const names = group.map((name) => JSON.stringify(name))
    return names.length === 1 ? names[0] : `(${names.join(' AND ')})`
  })
  return parts.length > 1 ? `(${parts.join(' OR ')})` : parts[0] ?? ''
}

export function labelGroupsFromQuery(tokens: string[]): LabelGroups {
  const requirements = (node: LabelNode): string[] => {
    if ('name' in node) return [node.name]
    if (node.operator === 'AND') return node.children.flatMap(requirements)
    throw new Error('This expression cannot be edited as a list of label groups')
  }
  const alternatives = (node: LabelNode): LabelGroups => {
    if (!('name' in node) && node.operator === 'OR') return node.children.flatMap(alternatives)
    // Preserve distinct names: JavaScript lowercasing differs from the server's Unicode case folding.
    return [[...new Set(requirements(node))]]
  }
  const expressions = tokens.filter((token) => token.startsWith('label:')).map((token) => parseLabels(token.slice(6)))
  if (!expressions.length) return []
  // Repeated label qualifiers are combined with AND by the server.
  return alternatives(expressions.length === 1 ? expressions[0] : { operator: 'AND', children: expressions })
}

export function parseLabels(raw: string): LabelNode {
  // The server validates queries too; keep this grammar aligned with workitems/labels.go.
  const tokens = raw.match(/"(?:\\.|[^"\\])*"|[()]|[^\s()"]+/gu) ?? []
  if (tokens.join('') !== raw.replace(/"(?:\\.|[^"\\])*"|\s/gu, (part) => part.startsWith('"') ? part : '')) throw new Error('Invalid quoted label')
  let position = 0, count = 0
  const atom = (depth: number): LabelNode => {
    if (depth > 8 || ++count > 200) throw new Error('Label expression is too complex')
    const token = tokens[position++]
    if (token === '(') {
      const group = expression(depth + 1, false)
      if (tokens[position++] !== ')') throw new Error('Unclosed label group')
      return group
    }
    if (!token || ['AND', 'OR', 'NOT', ')'].includes(token)) throw new Error('Expected a label name')
    const name: string = token.startsWith('"') ? JSON.parse(token) : token
    if (!name.trim()) throw new Error('Expected a nonempty label name')
    return { name }
  }
  const expression = (depth: number, andOnly: boolean): LabelNode => {
    const operator = andOnly ? 'AND' : 'OR'
    const children = [andOnly ? atom(depth) : expression(depth, true)]
    while (tokens[position] === operator) {
      position++
      children.push(andOnly ? atom(depth) : expression(depth, true))
    }
    return children.length === 1 ? children[0] : { operator, children }
  }
  const node = expression(0, false)
  if (position !== tokens.length) throw new Error('Expected AND or OR between labels')
  return node
}
