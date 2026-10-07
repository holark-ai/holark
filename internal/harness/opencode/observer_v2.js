import { appendFile } from "node:fs/promises"

const identifier = "[A-Za-z0-9_-]{1,128}"
const taskEnvelope = new RegExp(
  '^<task id="(' + identifier + ')" state="(completed|error)">\\n' +
    '(?:<summary>[^\\r\\n]*<\\/summary>\\n)?' +
    '<(task_result|task_error)>\\n[\\s\\S]*\\n<\\/\\3>\\n<\\/task>$',
)

// V2 server events carry their payload in `data`; V1 events use `properties`.
// Accept either so the normalizer emits identical facts on both APIs.
function payloadOf(event) {
  return event.data ?? event.properties ?? {}
}

function interaction(event, opened, kind) {
  const payload = payloadOf(event)
  const sessionID = payload.sessionID
  const requestID = payload.requestID ?? payload.id ?? payload.permissionID
  if (typeof sessionID !== "string" || typeof requestID !== "string") return
  return {
    type: kind + (opened ? "_opened" : "_closed"),
    session_id: sessionID,
    request_id: requestID,
  }
}

function contextUsage(event) {
  const info = payloadOf(event).info
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
  const payload = payloadOf(event)
  switch (event.type) {
    case "message.updated":
      return contextUsage(event)
    case "session.forked":
      if (typeof payload.sessionID !== "string") return
      // parentID identifies copied history; the fork is a new root session.
      return { type: "session_created", session_id: payload.sessionID, parent_id: "" }
    case "session.created": {
      const sessionID = payload.sessionID ?? payload.info?.id
      const parentID = payload.parentID ?? payload.info?.parentID
      if (typeof sessionID !== "string") return
      return {
        type: "session_created",
        session_id: sessionID,
        parent_id: typeof parentID === "string" ? parentID : "",
      }
    }
    case "session.execution.started":
      if (typeof payload.sessionID !== "string") return
      return { type: "session_status", session_id: payload.sessionID, status: "busy" }
    case "session.execution.failed":
    case "session.execution.interrupted":
      if (typeof payload.sessionID !== "string") return
      // V2 emits one terminal event. Record the outcome before settling the
      // turn so failure/interruption cannot become a successful completion.
      return [
        { type: "session_error", session_id: payload.sessionID, outcome: event.type === "session.execution.interrupted" ? "interrupted" : "error" },
        { type: "session_status", session_id: payload.sessionID, status: "idle" },
      ]
    case "session.error":
      if (typeof payload.sessionID === "string") return { type: "session_error", session_id: payload.sessionID, outcome: payload.error?.name === "MessageAbortedError" ? "interrupted" : "error" }
      return
    case "session.status": {
      const status = payload.status?.type
      if (typeof payload.sessionID !== "string" || !["busy", "retry", "idle"].includes(status)) return
      return { type: "session_status", session_id: payload.sessionID, status }
    }
    case "session.execution.succeeded":
    case "session.idle":
      if (typeof payload.sessionID !== "string") return
      return { type: "session_status", session_id: payload.sessionID, status: "idle" }
    case "form.created":
    case "form.replied":
    case "form.cancelled": {
      const opened = event.type === "form.created"
      const form = opened ? payload.form : payload
      // MCP forms can have a non-session owner ("global").
      if (typeof form?.sessionID !== "string" || !form.sessionID.startsWith("ses_") || typeof form.id !== "string") return
      return {
        type: opened ? "question_opened" : "question_closed",
        session_id: form.sessionID,
        request_id: form.id,
      }
    }
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
      const part = payload.part
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
// Normalization can produce an ordered batch of facts for a single event.
export function writer() {
  let queue = Promise.resolve()
  return (fact) => {
    queue = queue.catch(() => {}).then(() => appendFile(process.env.HOLARK_OPENCODE_EVENTS_PATH,
      (Array.isArray(fact) ? fact : [fact]).map((entry) => JSON.stringify(entry) + "\n").join(""),
      { encoding: "utf8", flag: "a" }))
    return queue
  }
}
