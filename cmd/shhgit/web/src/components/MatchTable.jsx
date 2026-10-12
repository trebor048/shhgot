import React, { useState } from 'react'
import { ChevronDown, Copy, Eye, EyeOff, FileText, Sparkles } from 'lucide-react'
import { SEVERITY, severityMeta, short, shortRepo } from '../lib/format'
import { useToast } from './Toast'

// Ported from shhgit3's embedded dashboard Matches tab (the `.mrow` card design
// in web.go): compact priority-coded rows that expand into a secret strip, a
// metadata row and the captured token list. Kept as React state/JSX instead of
// the original string/DOM building, with the same layout and behaviour.

const MAX_ROWS = 500

const PRIORITY = {
  3: {
    row: 'border-l-red-500',
    dot: 'bg-red-500 shadow-[0_0_6px_rgba(239,68,68,0.85)]',
    sig: 'text-red-400',
    label: 'CRIT',
    name: 'Critical',
  },
  2: { row: 'border-l-orange-500', dot: 'bg-orange-500', sig: 'text-orange-400', label: 'HIGH', name: 'High' },
  1: { row: 'border-l-yellow-500', dot: 'bg-yellow-500', sig: 'text-yellow-300', label: 'MED', name: 'Medium' },
  0: { row: 'border-l-green-500', dot: 'bg-green-500', sig: 'text-green-400', label: 'LOW', name: 'Low' },
}

// Most- to least-severe, matching the SEVERITY map's insertion order.
const SEVERITY_KEYS = Object.keys(SEVERITY)

function prio(p) {
  return PRIORITY[p] || PRIORITY[0]
}

function MatchCard({ m, onViewFile, onReview }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const [hiddenSecret, setHiddenSecret] = useState(false)
  const p = prio(m.priority)
  const sev = severityMeta(m.severity)

  const secret = m.secret || (Array.isArray(m.matches) && m.matches[0]) || ''
  const toks = Array.isArray(m.matches) ? m.matches : []

  const copy = (v, label) => {
    // navigator.clipboard is undefined on a non-secure origin; guard so the
    // click shows the error toast instead of throwing a TypeError.
    if (!navigator.clipboard) {
      toast('Clipboard unavailable', 'error')
      return
    }
    navigator.clipboard
      .writeText(v == null ? '' : String(v))
      .then(() => toast(label ? `${label} copied` : 'Copied', 'ok'), () => toast('Clipboard unavailable', 'error'))
  }

  return (
    <div className={`rounded-lg mb-1.5 overflow-hidden border border-slate-800 border-l-[3px] bg-slate-900/50 hover:shadow-[0_0_8px_rgba(34,211,238,0.1)] transition ${p.row}`}>
      {/* Head / collapsed row */}
      <div
        onClick={() => setOpen((o) => !o)}
        className="grid items-center gap-2 px-2.5 py-1.5 cursor-pointer hover:bg-slate-800/50 transition-colors
                   grid-cols-[10px_64px_minmax(110px,1.2fr)_minmax(100px,1fr)_minmax(130px,1.6fr)_46px_62px_14px]"
      >
        <span
          className={`w-2 h-2 rounded-full justify-self-center shrink-0 ${p.dot}`}
          role="img"
          title={`rule priority: ${p.name} (${m.priority})`}
          aria-label={`rule priority: ${p.name}`}
        />
        {sev ? (
          <span
            className={`justify-self-center shrink-0 text-[9px] font-bold uppercase tracking-wide px-1.5 py-0.5 rounded border ${sev.badge}`}
            role="img"
            title={`verified severity: ${sev.label}`}
            aria-label={`verified severity: ${sev.label}`}
          >
            {sev.label}
          </span>
        ) : (
          <span aria-hidden="true" className="justify-self-center text-[9px] text-slate-600">
            {'\u2014'}
          </span>
        )}
        <span className={`font-semibold text-xs truncate ${p.sig}`} title={m.signature}>
          {m.signature}
        </span>
        <span className="text-[11px] text-cyan-400 truncate" title={m.url}>
          {short(shortRepo(m.url), 32)}
        </span>
        <span className="text-[10.5px] text-orange-300 font-mono truncate flex items-center gap-1.5" title={m.file}>
          <span className="truncate">{short(m.file, 36)}</span>
          {m.line ? (
            <span className="shrink-0 text-[10px] text-yellow-300 bg-yellow-500/10 border border-yellow-500/30 px-1.5 rounded-full font-mono">
              L{m.line}
            </span>
          ) : null}
        </span>
        <span className="text-[10px] text-slate-400 text-right whitespace-nowrap">
          {'\u2605'} {m.stars > 0 ? m.stars.toLocaleString() : 0}
        </span>
        <span className="text-[10px] text-slate-500 text-right whitespace-nowrap">
          {(() => {
            const t = new Date(m.timestamp)
            return Number.isNaN(t.getTime()) ? '—' : t.toLocaleTimeString()
          })()}
        </span>
        <ChevronDown
          size={12}
          className={`justify-self-center text-slate-500 transition-transform ${open ? 'rotate-180' : ''}`}
        />
      </div>

      {/* Expanded body */}
      {open && (
        <div className="border-t border-slate-800 bg-slate-950/60">
          {/* Secret strip */}
          <div className="flex items-center gap-2 px-2.5 py-1 bg-green-500/5 border-b border-slate-900">
            <span className="text-[9px] font-bold uppercase tracking-wider text-green-400 shrink-0">
              {'\u{1F511}'} secret
            </span>
            <code
              className={`flex-1 min-w-0 font-mono text-[10.5px] text-green-400 break-all transition ${
                hiddenSecret ? 'blur-[7px] select-none' : ''
              }`}
              title={secret}
            >
              {short(secret || '(no captured value)', 160)}
            </code>
            <button
              onClick={() => copy(secret, 'Secret')}
              title="Copy secret"
              className="shrink-0 p-1 rounded text-slate-400 hover:text-cyan-300 hover:bg-slate-800"
            >
              <Copy size={13} />
            </button>
            <button
              onClick={() => setHiddenSecret((h) => !h)}
              title={hiddenSecret ? 'Reveal secret' : 'Hide secret'}
              className="shrink-0 flex items-center gap-1 text-[11px] px-2 py-1 rounded border border-slate-700 text-slate-400 hover:text-yellow-300 hover:border-yellow-500/50"
            >
              {hiddenSecret ? <Eye size={12} /> : <EyeOff size={12} />}
              {hiddenSecret ? 'show' : 'hide'}
            </button>
          </div>

          {/* Meta row */}
          <div className="flex items-center gap-2.5 flex-wrap text-[10.5px] text-slate-400 px-2.5 pt-1.5">
            <span>
              source: <b className="text-slate-300">{m.source || 'unknown'}</b>
            </span>
            <span>
              verified severity:{' '}
              <b className={sev ? sev.text : 'text-slate-300'}>{sev ? sev.label : 'unknown'}</b>
            </span>
            <span>
              rule priority: <b className="text-slate-300">{p.name}</b>
            </span>
            {m.has_file && m.line ? (
              <span>
                line <b className="text-slate-300">{m.line}</b>
              </span>
            ) : null}
            <a
              href={/^https?:\/\//i.test(m.url || '') ? m.url : undefined}
              target="_blank"
              rel="noreferrer"
              className="text-cyan-400 underline break-all"
            >
              {m.url}
            </a>
            {m.has_file && onViewFile && (
              <button
                onClick={() => onViewFile(m)}
                className="inline-flex items-center gap-1 text-cyan-400 hover:text-cyan-200"
                title="View the captured file with the secret highlighted"
              >
                <FileText size={12} />
                view full file
              </button>
            )}
            {onReview && (
              <button
                onClick={() => onReview(m)}
                className="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-slate-700 text-cyan-400 hover:text-white hover:border-cyan-500 hover:shadow-[0_0_10px_rgba(34,211,238,0.35)] transition"
                title="AI review: secret + file + repo"
              >
                <Sparkles size={12} />
                review
              </button>
            )}
          </div>

          {/* Token list */}
          {toks.length > 0 && (
            <div className="space-y-1 px-2.5 py-2">
              {toks.map((t, i) => (
                <div
                  key={i}
                  className="flex items-center gap-2 bg-slate-950 border border-slate-800 rounded-md px-2 py-1 font-mono text-[11.5px] break-all hover:border-green-500/40 transition-colors"
                >
                  <span className="flex-1 min-w-0 text-green-400">{t}</span>
                  <button
                    onClick={() => copy(t, 'Token')}
                    title="Copy token"
                    className="shrink-0 p-1 rounded text-slate-400 hover:text-cyan-300 hover:bg-slate-800"
                  >
                    <Copy size={13} />
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export default function MatchTable({
  matches,
  totalMatches = 0,
  filters = {},
  setFilters,
  paused = false,
  onTogglePause,
  onViewFile,
  onReview,
}) {
  const hasFilters = Object.values(filters).some((f) => f)
  const shown = matches.slice(0, MAX_ROWS)

  return (
    <div>
      {/* Toolbar */}
      <div className="flex items-center gap-2 text-xs text-slate-500 pb-2 px-1">
        <span>
          {matches.length.toLocaleString()} matches / {totalMatches.toLocaleString()} total
          {matches.length > shown.length && ` \u00b7 showing first ${MAX_ROWS}`}
        </span>
        <span className="flex-1" />
        {setFilters && (
          <label className="flex items-center gap-1.5" htmlFor="match-severity-filter">
            <span className="uppercase tracking-wide">verified severity</span>
            <select
              id="match-severity-filter"
              value={filters.severity || ''}
              onChange={(e) => setFilters((f) => ({ ...f, severity: e.target.value }))}
              className="px-1.5 py-1 rounded border border-slate-700 bg-slate-950 text-slate-200 focus:outline-none focus:border-cyan-500"
            >
              <option value="">all</option>
              {SEVERITY_KEYS.map((s) => (
                <option key={s} value={s}>
                  {severityMeta(s).label}
                </option>
              ))}
            </select>
          </label>
        )}
        {hasFilters && setFilters && (
          <button
            onClick={() => setFilters({ source: '', signature: '', priority: '', search: '', severity: '' })}
            className="px-2.5 py-1 rounded border border-slate-700 text-slate-400 hover:text-slate-200"
          >
            Clear filters
          </button>
        )}
        {onTogglePause && (
          <label className="flex items-center gap-1.5 cursor-pointer">
            <input type="checkbox" checked={paused} onChange={onTogglePause} className="accent-cyan-500" />
            pause
          </label>
        )}
      </div>

      {/* Cards */}
      {matches.length === 0 ? (
        <div className="text-center text-slate-500 py-16 italic">
          {totalMatches > 0 ? 'No matches for the current filters.' : 'No matches yet - waiting for the scanner\u2026'}
        </div>
      ) : (
        shown.map((m, i) => (
          <MatchCard
            key={m.id || `${m.signature}-${m.url}-${m.file}-${i}`}
            m={m}
            onViewFile={onViewFile}
            onReview={onReview}
          />
        ))
      )}
    </div>
  )
}
