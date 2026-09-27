import { useEffect, useRef, useState } from 'react'
import { api } from './api'

// Client-side caps mirror the server ring buffers (maxMatches/maxLogs/maxTokens
// in web.go) so a long-running dashboard cannot grow without bound.
export const MAX_MATCHES = 10000
export const MAX_LOGS = 2000
export const MAX_TOKENS = 2000

const EMPTY_STATS = {
  total_matches: 0,
  matches_by_source: {},
  matches_by_signature: {},
  matches_by_priority: {},
  top_signatures: [],
  last_updated: null,
}

const ACTIVITY_EMPTY = {
  fetching: [],
  scanning: [],
  total_fetched: 0,
  total_cloned: 0,
  total_scanned: 0,
  total_failed: 0,
  rate_limited: 0,
}

function cap(arr, max) {
  return arr.length > max ? arr.slice(arr.length - max) : arr
}

// useFeed subscribes to /api/events (Server-Sent Events) and keeps the live
// match list, stats, log tail, token results and activity snapshot in sync.
//
// Live pushes are the source of truth; the initial /api/ws + /api/logs +
// /api/tokens fetches only matter for the brief window before the stream opens
// (and for the case where the stream is blocked by a proxy). Every reconnect
// re-sends a snapshot from the server, so the client resyncs itself.
export function useFeed() {
  const [matches, setMatches] = useState([])
  const [stats, setStats] = useState(EMPTY_STATS)
  const [logs, setLogs] = useState([])
  const [tokens, setTokens] = useState([])
  const [activity, setActivity] = useState(ACTIVITY_EMPTY)
  const [connected, setConnected] = useState(false)
  const [lastReview, setLastReview] = useState(null)
  const [reviewTick, setReviewTick] = useState(0)

  // High-frequency streams (logs can arrive many times a second) are buffered
  // and flushed on a timer so React is not asked to re-render per line.
  const pendingLogs = useRef([])
  const pendingTokens = useRef([])
  const flushTimer = useRef(null)

  useEffect(() => {
    let cancelled = false

    // Initial state for the tabs the snapshot event does not carry.
    Promise.all([
      api.snapshot().catch(() => null),
      api.logs().catch(() => null),
      api.tokens().catch(() => null),
      api.activity().catch(() => null),
    ]).then(([snap, logRes, tokRes, actRes]) => {
      if (cancelled) return
      if (snap) {
        if (Array.isArray(snap.matches)) setMatches(snap.matches)
        if (snap.stats) setStats(snap.stats)
      }
      if (logRes && Array.isArray(logRes.logs)) setLogs(cap(logRes.logs, MAX_LOGS))
      if (tokRes && Array.isArray(tokRes.tokens)) setTokens(cap(tokRes.tokens, MAX_TOKENS))
      if (actRes) setActivity(actRes)
    })

    flushTimer.current = setInterval(() => {
      if (pendingLogs.current.length) {
        const add = pendingLogs.current
        pendingLogs.current = []
        setLogs((prev) => cap(prev.concat(add), MAX_LOGS))
      }
      if (pendingTokens.current.length) {
        const add = pendingTokens.current
        pendingTokens.current = []
        setTokens((prev) => cap(prev.concat(add), MAX_TOKENS))
      }
    }, 250)

    const es = new EventSource('/api/events')

    es.onopen = () => setConnected(true)
    es.onerror = () => {
      // EventSource reconnects on its own; reflect the gap in the UI only.
      setConnected(false)
    }
    es.onmessage = (ev) => {
      let data
      try {
        data = JSON.parse(ev.data)
      } catch {
        return
      }
      switch (data.type) {
        case 'snapshot':
          setConnected(true)
          if (Array.isArray(data.matches)) setMatches(cap(data.matches, MAX_MATCHES))
          if (data.stats) setStats(data.stats)
          if (data.activity) setActivity(data.activity)
          break
        case 'match':
          if (data.match) setMatches((prev) => cap(prev.concat([data.match]), MAX_MATCHES))
          if (data.stats) setStats(data.stats)
          break
        case 'log':
          if (data.log != null) pendingLogs.current.push(data.log)
          break
        case 'token':
          if (data.token) pendingTokens.current.push(data.token)
          break
        case 'activity':
          if (data.activity) setActivity(data.activity)
          break
        case 'review':
          if (data.review) {
            setLastReview(data.review)
            setReviewTick((n) => n + 1)
          }
          break
        default:
          break
      }
    }

    return () => {
      cancelled = true
      if (flushTimer.current) clearInterval(flushTimer.current)
      es.close()
    }
  }, [])

  return { matches, stats, logs, tokens, activity, connected, lastReview, reviewTick }
}
