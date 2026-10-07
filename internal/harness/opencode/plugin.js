import { normalize, writer } from "../observer.js"

export const HolarkObserver = async () => {
  if (process.env.HOLARK_OPENCODE_OBSERVER !== "server") return {}
  const append = writer()
  await append({ type: "observer_initialized" })
  return {
    event: async ({ event }) => {
      const fact = normalize(event)
      if (fact) await append(fact)
    },
  }
}
