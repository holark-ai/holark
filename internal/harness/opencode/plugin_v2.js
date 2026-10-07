import { normalize, writer } from "../observer.js"

// Server-side observer for OpenCode >= 2.0.0. V1's `event` hook becomes a
// `ctx.event.subscribe()` async iteration (see
// https://opencode.ai/v2/docs/build/plugins/migrate-v1); the emitted facts
// keep the V1 shapes so the monitor and reducer are unchanged.
// Plugin.define is an identity helper; a plain definition avoids requiring an
// npm installation for this injected local plugin.
export default {
  id: "holark-observer",
  setup(ctx) {
    if (process.env.HOLARK_OPENCODE_OBSERVER !== "server") return
    const append = writer()
    const controller = new AbortController()
    void (async () => {
      try {
        await append({ type: "observer_initialized" })
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          if (process.env.HOLARK_OPENCODE_OBSERVER !== "server") break
          const fact = normalize(event)
          if (fact) await append(fact)
        }
      } catch (error) {
        // Aborted subscriptions are normal plugin cleanup; anything else is an
        // explicit failure so the monitor degrades instead of hanging.
        if (controller.signal.aborted) return
        await append({ type: "observer_failed" }).catch(() => {})
      }
    })()
    return () => controller.abort()
  },
}
