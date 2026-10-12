import React, { useEffect, useState } from 'react'
import { AlertTriangle, ShieldAlert, ShieldCheck } from 'lucide-react'
import { api } from '../lib/api'
import { relTime, safeUrl, shortRepo } from '../lib/format'

// Suppressed findings are the ones the AI false-positive filter removed before
// they reached the dashboard or any webhook. The ring buffer is server-side and
// bounded, so this panel is read-only and polls rather than subscribing to the
// live feed (the filter has no event in /api/events).
const REFRESH_MS = 5000

// normalize accepts the endpoint's envelope ({suppressed, suppressed_total,
// suppressed_overflow}) or, for backward compatibility, a bare JSON array or
// an {items, total, overflow} envelope.
function normalize(payload) {
  if (Array.isArray(payload)) return { items: payload, overflow: 0, total: null }
  if (payload && typeof payload === 'object') {
    const items = Array.isArray(payload.suppressed)
      ? payload.suppressed
      : Array.isArray(payload.items)
        ? payload.items
        : []
    const total = Number.isFinite(payload.suppressed_total)
      ? payload.suppressed_total
      : Number.isFinite(payload.total)
        ? payload.total
        : null
    let overflow = Number.isFinite(payload.overflow)
      ? payload.overflow
      : Number.isFinite(payload.suppressed_overflow)
        ? payload.suppressed_overflow
        : 0
    if (overflow <= 0 && total != null && total > items.length) overflow = total - items.length
    return { items, overflow, total }
  }
  return { items: [], overflow: 0, total: null }
}

function SuppressedRow({ item }) {
  const url = safeUrl(item.url)
  const reason = item.reason || 'unknown reason'
  return (
    <div className="rounded-lg border border-slate-800 border-l-[3px] border-l-slate-500 bg-slate-900/50 px-3 py-2">
      <div className="flex items-center gap-2 flex-wrap">
        <ShieldAlert size={13} className="text-slate-400 shrink-0" aria-hidden="true" />
        <span className="text-xs font-semibold text-slate-200 truncate" title={item.signature}>
          {item.signature || 'unknown signature'}
        </span>
        <span
          className="text-[10px] px-1.5 py-0.5 rounded border border-slate-600 bg-slate-800 text-slate-300"
          title={item.stage ? `stage: ${item.stage}` : 'suppression reason'}
        >
          {reason}
        </span>
        {item.stage ? <span className="text-[10px] text-slate-500">{item.stage}</span> : null}
        <span className="flex-1" />
        {item.timestamp ? (
          <span className="text-[10px] text-slate-500 shrink-0">{relTime(item.timestamp)}</span>
        ) : null}
      </div>
      <div className="mt-1 flex items-center gap-2 flex-wrap text-[10.5px] text-slate-400">
        {item.file ? (
          <span className="font-mono text-orange-300 break-all" title={item.file}>
            {item.file}
          </span>
        ) : null}
        {url ? (
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="text-cyan-400 underline break-all"
            title={item.url}
          >
            {shortRepo(item.url)}
          </a>
        ) : item.url ? (
          <span className="break-all" title={item.url}>
            {item.url}
          </span>
        ) : null}
      </div>
    </div>
  )
}

export default function SuppressedPanel() {
  const [state, setState] = useState({ items: [], overflow: 0, total: null })
  const [error, setError] = useState('')
  const [degraded, setDegraded] = useState(false)

  useEffect(() => {
    let cancelled = false
    const load = () => {
      api
        .suppressed()
        .then((payload) => {
          if (cancelled) return
          setState(normalize(payload))
          setError('')
        })
        .catch((err) => {
          if (cancelled) return
          setError(err.message || 'Could not load suppressed findings')
        })
      // The live feed's stats snapshot does not carry the filter's health, so
      // the degraded flag is read from /api/stats directly.
      api
        .stats()
        .then((s) => {
          if (!cancelled) setDegraded(Boolean(s && s.ai_filter_degraded))
        })
        .catch(() => {})
    }
    load()
    const id = setInterval(load, REFRESH_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  const items = state.items

  return (
    <div className="space-y-3">
      {degraded && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-yellow-500/40 bg-yellow-500/10 px-3 py-2 text-xs text-yellow-200"
        >
          <AlertTriangle size={14} className="mt-0.5 shrink-0" />
          <span>
            The AI filter is <b>degraded</b>: the classifier has failed repeatedly, so its verdicts
            are unreliable and some findings below may have been filtered by mistake.
          </span>
        </div>
      )}

      {error && (
        <div
          role="alert"
          className="rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300"
        >
          {error}
        </div>
      )}

      {state.overflow > 0 && (
        <div
          role="status"
          className="rounded-lg border border-slate-700 bg-slate-900/60 px-3 py-2 text-xs text-slate-400"
        >
          Showing the most recent {items.length.toLocaleString()} suppressed findings.{' '}
          {state.overflow.toLocaleString()}{' '}
          {state.overflow === 1 ? 'older entry was' : 'older entries were'} evicted from the bounded
          ring buffer.
        </div>
      )}

      {items.length > 0 && (
        <div className="text-xs text-slate-500 px-1">
          {items.length.toLocaleString()} suppressed {items.length === 1 ? 'finding' : 'findings'}
          {state.total != null && state.total !== items.length
            ? ` / ${state.total.toLocaleString()} total`
            : ''}
        </div>
      )}

      {items.length === 0 ? (
        <div className="text-center text-slate-500 italic py-16">
          <ShieldCheck className="mx-auto mb-2 opacity-60" aria-hidden="true" />
          No suppressed findings — the AI filter is off or nothing was filtered.
        </div>
      ) : (
        <div className="space-y-1.5">
          {items
            .slice()
            .reverse()
            .map((s, i) => (
              <SuppressedRow key={`${s.signature}-${s.url}-${s.file}-${i}`} item={s} />
            ))}
        </div>
      )}
    </div>
  )
}
