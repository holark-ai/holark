export type ApiRequestTransport = (path: string, init?: RequestInit) => Promise<Response>

let requestTransport: ApiRequestTransport = (path, init) => fetch(path, init)

export function setApiRequestTransport(transport: ApiRequestTransport) {
  const previous = requestTransport
  requestTransport = transport
  return () => {
    if (requestTransport === transport) requestTransport = previous
  }
}

export function sendApiRequest(path: string, init?: RequestInit) {
  return requestTransport(path, init)
}
