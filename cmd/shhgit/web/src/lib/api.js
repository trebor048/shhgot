// Thin wrapper over the shhgit HTTP API.
//
// Every /api/... response is JSON, including errors, which are shaped
// {"error":"..."} (see writeJSONError in web_settings.go). apiFetch unwraps
// that so callers get a normal Error carrying the server's message.

async function apiFetch(path, opts = {}) {
  const res = await fetch(path, opts)
  const text = await res.text()
  let body = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = null
    }
  }
  if (!res.ok) {
    const msg = body && body.error ? body.error : `${res.status} ${res.statusText}`
    throw new Error(msg)
  }
  return body
}

export function getJSON(path) {
  return apiFetch(path)
}

export function postJSON(path, payload) {
  return apiFetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload ?? {}),
  })
}

export function putJSON(path, payload) {
  return apiFetch(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload ?? {}),
  })
}

export function del(path) {
  return apiFetch(path, { method: 'DELETE' })
}

// --- endpoint helpers -------------------------------------------------------

export const api = {
  snapshot: () => getJSON('/api/ws'),
  stats: () => getJSON('/api/stats'),
  matches: (filters = {}) => {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(filters)) {
      if (v !== '' && v != null) q.set(k, v)
    }
    const qs = q.toString()
    return getJSON(`/api/matches${qs ? `?${qs}` : ''}`)
  },
  logs: () => getJSON('/api/logs'),
  tokens: () => getJSON('/api/tokens'),
  signatures: () => getJSON('/api/signatures'),
  activity: () => getJSON('/api/activity'),
  skipRepo: (url) => postJSON('/api/activity/skip', { url }),
  scanProgress: () => getJSON('/api/scan/progress'),
  regexStats: () => getJSON('/api/stats/regex'),
  file: (id) => getJSON(`/api/file?id=${encodeURIComponent(id)}`),

  reviewList: () => getJSON('/api/review'),
  reviewGet: (id) => getJSON(`/api/review/${encodeURIComponent(id)}`),
  reviewCreate: (payload) => postJSON('/api/review', payload),
  reviewDelete: (id) => del(`/api/review/${encodeURIComponent(id)}`),

  settings: () => getJSON('/api/settings'),
  saveSettings: (payload) => putJSON('/api/settings', payload),
  testSettings: (payload) => postJSON('/api/settings/test', payload),
}

// streamURL is the EventSource target for a review's live assessment. Using
// EventSource (not fetch) keeps reconnection and the /api/review/<id>/stream
// SSE framing in the browser's hands.
export function reviewStreamURL(id) {
  return `/api/review/${encodeURIComponent(id)}/stream`
}

// ssePost performs a POST whose response is an SSE stream (the review chat
// endpoint). EventSource cannot POST, so the frames are read off the response
// body directly and handed to onEvent. Events are separated by a blank line and
// carry a single `data: {json}` line, matching sseSend in web_review.go.
export async function ssePost(path, payload, onEvent) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload ?? {}),
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    let msg = `${res.status} ${res.statusText}`
    try {
      const parsed = JSON.parse(text)
      if (parsed && parsed.error) msg = parsed.error
    } catch {
      /* keep the status text */
    }
    throw new Error(msg)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let sep
    while ((sep = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, sep)
      buf = buf.slice(sep + 2)
      for (const line of frame.split('\n')) {
        if (!line.startsWith('data:')) continue
        const json = line.slice(5).trim()
        if (!json) continue
        try {
          onEvent(JSON.parse(json))
        } catch {
          /* ignore malformed frames */
        }
      }
    }
  }
}
