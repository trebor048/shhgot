import React, { useEffect, useRef, useState } from 'react'
import { ArrowDownToLine, Trash2 } from 'lucide-react'
import { ansiToRuns, safeUrl } from '../lib/format'

export default function LogsPanel({ logs }) {
  const [follow, setFollow] = useState(true)
  // Anchor "Clear view" on the last line present at the moment of clearing
  // rather than on an index or count: the log buffer is a rolling window that
  // evicts old entries from the front, so a stored index goes stale as soon as
  // the cap is reached (it would hide the newest lines forever). Once the
  // anchor line itself is evicted, everything buffered is newer than the clear.
  const [clearedTail, setClearedTail] = useState(null)
  const bottomRef = useRef(null)

  useEffect(() => {
    if (follow && bottomRef.current) {
      bottomRef.current.scrollIntoView({ block: 'end' })
    }
  }, [logs, follow])

  const clear = () => setClearedTail(logs.length ? logs[logs.length - 1] : null)

  const cut = clearedTail == null ? -1 : logs.lastIndexOf(clearedTail)
  const visible = cut >= 0 ? logs.slice(cut + 1) : logs

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-2 pb-2 text-xs text-slate-500">
        <button
          onClick={() => setFollow((f) => !f)}
          className={`flex items-center gap-1.5 px-2.5 py-1 rounded border transition ${
            follow ? 'border-cyan-500/40 text-cyan-300 bg-cyan-500/10' : 'border-slate-700 text-slate-400'
          }`}
        >
          <ArrowDownToLine size={13} />
          {follow ? 'Following' : 'Paused'}
        </button>
        <button
          onClick={clear}
          className="flex items-center gap-1.5 px-2.5 py-1 rounded border border-slate-700 text-slate-400 hover:text-slate-200"
        >
          <Trash2 size={13} />
          Clear view
        </button>
        <span className="ml-auto">{logs.length.toLocaleString()} lines</span>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto bg-slate-950 border border-slate-800 rounded-lg p-2 font-mono text-[11.5px] leading-5">
        {visible.length === 0 && <div className="text-slate-600 italic p-2">No log lines yet.</div>}
        {visible.map((line, i) => (
          <div key={i} className="whitespace-pre-wrap break-all hover:bg-cyan-500/5 px-1">
            {ansiToRuns(line).map((run, j) => {
              const href = safeUrl(run.href)
              if (!href) {
                return (
                  <span key={j} className={run.className}>
                    {run.text}
                  </span>
                )
              }
              return (
                <a
                  key={j}
                  href={href}
                  target="_blank"
                  rel="noreferrer"
                  className={`${run.className} underline decoration-dotted hover:text-cyan-200`}
                >
                  {run.text}
                </a>
              )
            })}
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
