import React, { useMemo, useState } from 'react'
import { CheckCircle2, Copy, ShieldQuestion, XCircle } from 'lucide-react'
import { relTime, truncateMiddle } from '../lib/format'
import { useToast } from './Toast'

function CopyBtn({ value }) {
  const toast = useToast()
  return (
    <button
      title="Copy raw token"
      onClick={() =>
        navigator.clipboard.writeText(value).catch(() => toast('Clipboard unavailable', 'error'))
      }
      className="p-1.5 hover:bg-slate-700 rounded text-slate-400 hover:text-cyan-300"
    >
      <Copy size={14} />
    </button>
  )
}

export default function TokensPanel({ tokens }) {
  const [onlyValid, setOnlyValid] = useState(false)

  const shown = useMemo(
    () => (onlyValid ? tokens.filter((t) => t.valid) : tokens).slice().reverse(),
    [tokens, onlyValid],
  )
  const valid = tokens.filter((t) => t.valid).length

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 text-xs">
        <span className="text-green-400 flex items-center gap-1">
          <CheckCircle2 size={14} /> {valid} live
        </span>
        <span className="text-slate-400 flex items-center gap-1">
          <XCircle size={14} /> {tokens.length - valid} dead
        </span>
        <label className="ml-auto flex items-center gap-1.5 text-slate-400 cursor-pointer">
          <input
            type="checkbox"
            checked={onlyValid}
            onChange={(e) => setOnlyValid(e.target.checked)}
            className="accent-cyan-500"
          />
          only live
        </label>
      </div>

      {shown.length === 0 && (
        <div className="text-center text-slate-500 italic py-16">
          <ShieldQuestion className="mx-auto mb-2 opacity-60" />
          No validated tokens yet.
        </div>
      )}

      <div className="space-y-1.5">
        {shown.map((t, i) => (
          <div
            key={`${t.token}-${i}`}
            className={`flex items-center gap-3 rounded-lg border px-3 py-2 transition ${
              t.valid ? 'border-green-500/30 bg-green-500/5' : 'border-red-500/25 bg-red-500/5'
            }`}
          >
            <span className={`text-xs font-semibold shrink-0 ${t.valid ? 'text-green-400' : 'text-red-400'}`}>
              {t.provider || 'unknown'}
            </span>
            <code className={`flex-1 font-mono text-xs break-all ${t.valid ? 'text-green-300' : 'text-red-300'}`}>
              {truncateMiddle(t.token, 12, 8)}
            </code>
            <span className="text-[10px] text-slate-500 shrink-0 hidden sm:inline">{relTime(t.timestamp)}</span>
            <span
              className={`text-[10px] font-bold px-2 py-0.5 rounded shrink-0 ${
                t.valid ? 'text-green-300 bg-green-500/15' : 'text-red-300 bg-red-500/15'
              }`}
            >
              {t.valid ? 'LIVE' : 'DEAD'}
            </span>
            <CopyBtn value={t.token} />
          </div>
        ))}
      </div>
    </div>
  )
}
