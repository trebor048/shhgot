import React, { useEffect, useState } from 'react'
import { X } from 'lucide-react'
import { api } from '../lib/api'

// Highlight the captured secret inside the file line, escaping nothing because
// React renders text nodes. Falls back to the whole line when the exact secret
// substring is not present (the scanner may have matched a longer token).
function HighlitLine({ text, secret }) {
  if (!secret || !text.includes(secret)) return <>{text}</>
  const idx = text.indexOf(secret)
  return (
    <>
      {text.slice(0, idx)}
      <mark className="bg-red-500/30 text-red-200 rounded px-0.5">{secret}</mark>
      {text.slice(idx + secret.length)}
    </>
  )
}

export default function FileModal({ match, onClose }) {
  const [state, setState] = useState({ loading: true, error: '', data: null })

  useEffect(() => {
    let cancelled = false
    setState({ loading: true, error: '', data: null })
    api
      .file(match.id)
      .then((d) => !cancelled && setState({ loading: false, error: '', data: d }))
      .catch((e) => !cancelled && setState({ loading: false, error: e.message, data: null }))
    return () => {
      cancelled = true
    }
  }, [match.id])

  useEffect(() => {
    const onKey = (e) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const lines = state.data ? state.data.content.split('\n') : []

  return (
    <div className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-2 sm:p-6" onClick={onClose}>
      <div
        className="bg-slate-900 border border-slate-700 rounded-lg w-full max-w-5xl max-h-[90vh] flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-4 py-2.5 border-b border-slate-800">
          <div className="min-w-0">
            <div className="text-sm font-semibold text-orange-300 truncate">{state.data?.file || match.file}</div>
            <div className="text-[11px] text-slate-500 truncate">{state.data?.url || match.url}</div>
          </div>
          <div className="flex-1" />
          {state.data?.truncated && (
            <span className="text-[10px] px-2 py-0.5 rounded bg-yellow-500/15 text-yellow-300 border border-yellow-500/30">
              truncated
            </span>
          )}
          <button onClick={onClose} className="p-1.5 hover:bg-slate-800 rounded">
            <X size={18} />
          </button>
        </div>

        <div className="overflow-auto flex-1 bg-slate-950">
          {state.loading && <div className="p-6 text-slate-500">Loading file…</div>}
          {state.error && <div className="p-6 text-red-400">Could not load file: {state.error}</div>}
          {state.data && (
            <pre className="text-xs leading-5 font-mono">
              {lines.map((ln, i) => {
                const n = i + 1
                const isSecret = n === state.data.secret_line
                return (
                  <div key={i} className={`flex ${isSecret ? 'bg-red-500/10' : ''}`}>
                    <span className="select-none w-12 shrink-0 text-right pr-3 text-slate-600 border-r border-slate-800">
                      {n}
                    </span>
                    <span className="px-3 whitespace-pre-wrap break-all text-slate-300">
                      {isSecret ? <HighlitLine text={ln} secret={state.data.secret} /> : ln}
                    </span>
                  </div>
                )
              })}
            </pre>
          )}
        </div>
      </div>
    </div>
  )
}
