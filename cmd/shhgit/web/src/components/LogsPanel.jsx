import React, { useEffect, useRef, useState } from 'react'
import { ArrowDownToLine, Trash2 } from 'lucide-react'
import { ansiToRuns } from '../lib/format'

export default function LogsPanel({ logs }) {
  const [follow, setFollow] = useState(true)
  const [hidden, setHidden] = useState([])
  const bottomRef = useRef(null)

  useEffect(() => {
    if (follow && bottomRef.current) {
      bottomRef.current.scrollIntoView({ block: 'end' })
    }
  }, [logs, follow])

  const clear = () => setHidden(logs.slice())

  const visible = logs.slice(hidden.length)

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
            {ansiToRuns(line).map((run, j) => (
              <span key={j} className={run.className}>
                {run.text}
              </span>
            ))}
          </div>
        ))}
        <div ref={bottomRef} />
      </div>
    </div>
  )
}
