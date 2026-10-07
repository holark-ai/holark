import { appendFile } from "node:fs/promises"

const identifier = "[A-Za-z0-9_-]{1,128}"
const taskEnvelope = new RegExp(
  '^<task id="(' + identifier + ')" state="(completed|error)">\\n' +
    '(?:<summary>[^\\r\\n]*<\\/summary>\\n)?' +
    '<(task_result|task_error)>\\n[\\s\\S]*\\n<\\/\\3>\\n<\\/task>$',
)

function interaction(event, opened, kind) {
  const properties = event.properties ?? {}
  const sessionID = properties.sessionID
  const requestID = properties.requestID ?? properties.id ?? properties.permissionID
  if (typeof sessionID !== "string" || typeof requestID !== "string") return
  return {
    type: kind + (opened ? "_opened" : "_closed"),
    session_id: sessionID,
    request_id: requestID,
  }
}

function contextUsage(event) {
  const info = event.properties?.info
  if (info?.role !== "assistant" || info.time?.completed == null) return
  if (typeof info.id !== "string" || typeof info.sessionID !== "string") return
  const tokens = info.tokens
  if (!tokens || typeof tokens !== "object") return
  let total = tokens.total
  if (total === undefined) {
    const values = [tokens.input, tokens.output, tokens.reasoning, tokens.cache?.read, tokens.cache?.write]
    if (values.some((value) => value !== undefined && (!Number.isSafeInteger(value) || value < 0))) return
    total = values.reduce((sum, value) => sum + (value ?? 0), 0)
  }
  if (!Number.isSafeInteger(total) || total < 0) return
  return {
    type: "context_usage",
    session_id: info.sessionID,
    message_id: info.id,
    context_tokens: total,
  }
}

export function normalize(event) {
  if (!event || typeof event !== "object") return
  const properties = event.properties ?? {}
  switch (event.type) {
    case "message.updated":
      return contextUsage(event)
    case "session.created": {
      const info = properties.info ?? {}
      if (typeof info.id !== "string") return
      return {
        type: "session_created",
        session_id: info.id,
        parent_id: typeof info.parentID === "string" ? info.parentID : "",
      }
    }
    case "session.error":
      if (typeof properties.sessionID === "string") return { type: "session_error", session_id: properties.sessionID, outcome: properties.error?.name === "MessageAbortedError" ? "interrupted" : "error" }
      return
    case "session.status": {
      const status = properties.status?.type
      if (typeof properties.sessionID !== "string" || !["busy", "retry", "idle"].includes(status)) return
      return { type: "session_status", session_id: properties.sessionID, status }
    }
    case "session.idle":
      if (typeof properties.sessionID !== "string") return
      return { type: "session_status", session_id: properties.sessionID, status: "idle" }
    case "question.asked":
      return interaction(event, true, "question")
    case "question.replied":
    case "question.rejected":
      return interaction(event, false, "question")
    case "permission.asked":
      return interaction(event, true, "permission")
    case "permission.replied":
      return interaction(event, false, "permission")
    case "message.part.updated": {
      const part = properties.part
      if (!part || typeof part !== "object") return
      if (part.type === "tool" && part.tool === "task") {
        const metadata = part.state?.metadata
        if (
          metadata?.background === true &&
          typeof part.sessionID === "string" &&
          metadata.parentSessionId === part.sessionID &&
          typeof metadata.sessionId === "string" &&
          metadata.sessionId !== part.sessionID
        ) {
          return {
            type: "background_task_started",
            session_id: metadata.sessionId,
            parent_id: part.sessionID,
          }
        }
        return
      }
      if (part.type !== "text" || part.synthetic !== true || typeof part.text !== "string") return
      const match = taskEnvelope.exec(part.text)
      if (!match) return
      if (
        (match[2] === "completed" && match[3] !== "task_result") ||
        (match[2] === "error" && match[3] !== "task_error")
      ) return
      return {
        type: "background_task_settled",
        session_id: match[1],
        outcome: match[2],
      }
    }
  }
}

// Each launch chooses exactly one observer and one serialized spool writer.
export function writer() {
  let queue = Promise.resolve()
  return (fact) => {
    queue = queue.catch(() => {}).then(() => appendFile(process.env.HOLARK_OPENCODE_EVENTS_PATH,
      JSON.stringify(fact) + "\n", { encoding: "utf8", flag: "a" }))
    return queue
  }
}
