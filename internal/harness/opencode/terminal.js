import { normalize, writer } from "./observer.js"

const types = ["session.created", "session.updated", "session.error", "session.status", "session.idle",
  "message.updated", "message.part.updated", "question.asked", "question.replied", "question.rejected",
  "permission.asked", "permission.replied"]
const validID = (id) => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id)

export default {
  id: "holark.terminal-observer",
  tui: async (api) => {
    if (process.env.HOLARK_OPENCODE_OBSERVER !== "terminal") return
    const append = writer()
    let selected, pending, stopped = false, timer
    const disposers = []
    const hydrated = new Set()
    const usageMessages = new Set()
    const parents = new Map()
    const stop = () => {
      stopped = true
      clearInterval(timer)
      for (const dispose of disposers.splice(0)) dispose()
    }
    const fail = () => {
      stop()
      // A known, explicit failure makes the monitor degrade immediately.
      void append({ type: "observer_failed" }).catch(() => {})
    }
    const emit = (fact) => {
      if (!fact) return
      if (fact.type === "context_usage") {
        const key = JSON.stringify([fact.session_id, fact.message_id])
        if (usageMessages.has(key)) return
        usageMessages.add(key)
      }
      void append(fact).catch(fail)
    }
    const metadata = (id, visiting = new Set()) => {
      if (!validID(id) || visiting.has(id)) throw new Error("Invalid session ancestry")
      const info = api.state.session.get(id)
      if (!info) return undefined // Route hydration may follow navigation.
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
    const sync = () => {
      const route = api.route.current
      if (!route || typeof route.name !== "string") throw new Error("Invalid route")
      // Home and subagent routes retain the last resumable top-level identity.
      if (route.name !== "session") { pending = undefined; return }
      const id = route.params?.sessionID
      const info = metadata(id)
      if (!info) {
        if (pending?.id !== id) pending = { id, since: Date.now() }
        if (Date.now() - pending.since > 15000) throw new Error("Selected session metadata unavailable")
        return
      }
      pending = undefined
      if (info.parentID) return
      if (!hydrated.has(id)) {
        const status = api.state.session.status(id)?.type ?? "idle"
        if (!["busy", "retry", "idle"].includes(status)) throw new Error("Invalid session status")
        emit({ type: "session_status", session_id: id, status })
        for (const kind of ["permission", "question"]) {
          for (const request of api.state.session[kind](id)) {
            if (!validID(request.id) || request.sessionID !== id) throw new Error("Invalid interaction")
            emit({ type: kind + "_opened", session_id: id, request_id: request.id })
          }
        }
        hydrated.add(id)
      }
      // Messages can load after metadata, so reconcile history even while selected.
      // Only usage is imported from history; it never establishes a new turn.
      for (const info of api.state.session.messages(id)) emit(normalize({ type: "message.updated", properties: { info } }))
      if (id === selected) return
      emit({ type: "session_selected", session_id: id })
      selected = id
    }
    const check = () => {
      if (stopped) return
      try { sync() } catch { fail() }
    }
    try {
      // Subscribe before synchronization so initial hydration cannot lose events.
      for (const type of types) disposers.push(api.event.on(type, (event) => {
        if (stopped) return
        try {
          const fact = normalize(event)
          if (fact?.session_id) metadata(fact.session_id)
          emit(fact)
          sync()
        } catch { fail() }
      }))
      api.lifecycle.onDispose(stop)
      await append({ type: "observer_initialized", status: "terminal" })
      check()
      if (!stopped) timer = setInterval(check, 50)
    } catch { fail() }
  },
}
