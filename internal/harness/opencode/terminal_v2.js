import { normalize, writer } from "./observer.js"

const validID = (id) => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id)

// V2 CLI plugins use callback events and cached session data, independently of
// the server plugin API. Keep the facts and root-selection policy used by V1.
export default {
  id: "holark.terminal-observer",
  setup(ctx) {
    if (process.env.HOLARK_OPENCODE_OBSERVER !== "terminal") return
    const append = writer()
    let selected, pending, stopped = false, checking = false, timer, unsubscribe
    const hydrated = new Set()
    const usageMessages = new Set()
    const parents = new Map()
    const stop = () => {
      stopped = true
      clearInterval(timer)
      unsubscribe?.()
      unsubscribe = undefined
    }
    const fail = () => {
      if (stopped) return
      stop()
      void append({ type: "observer_failed" }).catch(() => {})
    }
    const emit = (facts) => {
      if (!facts || stopped) return
      for (const fact of Array.isArray(facts) ? facts : [facts]) {
        if (fact.type === "context_usage") {
          const key = JSON.stringify([fact.session_id, fact.message_id])
          if (usageMessages.has(key)) continue
          usageMessages.add(key)
        }
        void append(fact).catch(fail)
      }
    }
    const metadata = (id, visiting = new Set()) => {
      if (!validID(id) || visiting.has(id)) throw new Error("Invalid session ancestry")
      const info = ctx.data.session.get(id)
      if (!info) return
      if (info.id !== id || (info.parentID && !validID(info.parentID))) throw new Error("Invalid session")
      visiting.add(id)
      if (info.parentID) metadata(info.parentID, visiting)
      const parent = info.parentID ?? ""
      if (!parents.has(id) || parents.get(id) !== parent) {
        emit({ type: "session_created", session_id: id, parent_id: parent })
        parents.set(id, parent)
      }
      return info
    }
    const sync = async () => {
      const route = ctx.ui.router.current()
      if (!route || typeof route.type !== "string") throw new Error("Invalid route")
      // Home and subagent routes retain the last resumable top-level identity.
      if (route.type !== "session") { pending = undefined; return }
      const id = route.sessionID
      if (!validID(id)) throw new Error("Invalid session")
      const session = ctx.data.session
      if (!hydrated.has(id)) {
        if (pending?.id !== id) pending = { id, since: Date.now() }
        try {
          if (!session.get(id)) await session.sync(id)
          const info = metadata(id)
          if (!info) throw new Error("Selected session metadata unavailable")
          if (info.parentID) { pending = undefined; return }
          await Promise.all([session.sync(id), session.permission.sync(id), session.form.sync(id), session.message.sync(id)])
        } catch (error) {
          // Fresh routes may exist optimistically before creation reaches the
          // server. Allow the same hydration grace period as the V1 observer.
          if (Date.now() - pending.since > 15000) throw error
          return
        }
        if (stopped) return
        pending = undefined
        const status = session.status(id)
        if (!["running", "idle"].includes(status)) throw new Error("Invalid session status")
        emit({ type: "session_status", session_id: id, status: status === "running" ? "busy" : "idle" })
        for (const [kind, requests] of [["permission", session.permission.list(id)], ["question", session.form.list(id)]]) {
          for (const request of requests ?? []) {
            if (!validID(request.id) || request.sessionID !== id) throw new Error("Invalid interaction")
            emit({ type: kind + "_opened", session_id: id, request_id: request.id })
          }
        }
        hydrated.add(id)
      }
      // Import only usage from completed history, never a new turn. V2 messages
      // use `type` for the role and inherit their session ID from the collection.
      for (const message of session.message.list(id)) {
        emit(normalize({ type: "message.updated", data: { info: { ...message, role: message.type, sessionID: id } } }))
      }
      // Navigation may have changed while the initial snapshot was loading.
      const current = ctx.ui.router.current()
      if (current.type !== "session" || current.sessionID !== id || selected === id) return
      emit({ type: "session_selected", session_id: id })
      selected = id
    }
    const check = async () => {
      if (stopped || checking) return
      checking = true
      try { await sync() } catch { fail() } finally { checking = false }
    }
    // Queue initialization before subscribing; the writer preserves fact order.
    void append({ type: "observer_initialized", status: "terminal" }).catch(fail)
    try {
      // V2 data.listen delivers { details } to a callback and returns the
      // unsubscribe function consumed by stop(), not an async iterator.
      // https://opencode.ai/v2/docs/build/plugins/cli#events
      unsubscribe = ctx.data.listen(({ details }) => {
        if (stopped) return
        try {
          const facts = normalize(details)
          for (const fact of (Array.isArray(facts) ? facts : [facts])) {
            if (fact?.session_id) metadata(fact.session_id)
          }
          emit(facts)
          void check()
        } catch { fail() }
      })
      void check()
      if (!stopped) timer = setInterval(check, 50)
    } catch { fail() }
    return stop
  },
}
