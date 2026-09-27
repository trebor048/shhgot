import React, { useEffect, useState } from 'react'
import { Ban, Cpu, Download, FileSearch, Gauge } from 'lucide-react'
import { api } from '../lib/api'
import { fmtDuration, shortRepo } from '../lib/format'
import { useToast } from './Toast'

function Stat({ label, value, color = 'text-cyan-400' }) {
  return (
    <div className="bg-slate-900/60 border border-slate-800 rounded-lg px-3 py-2">
      <div className={`text-lg font-bold ${color}`}>{value}</div>
      <div className="text-[11px] text-slate-500">{label}</div>
    </div>
  )
}

function RepoRow({ item, phase, onSkip }) {
  return (
    <div className="flex items-center gap-2 bg-slate-900/50 border border-slate-800 rounded px-2.5 py-1.5 mb-1">
      <span
        className={`text-[10px] font-bold px-1.5 py-0.5 rounded shrink-0 ${
          phase === 'fetching' ? 'text-cyan-300 bg-cyan-500/15' : 'text-yellow-300 bg-yellow-500/15'
        }`}
      >
        {phase === 'fetching' ? 'CLONE' : 'SCAN'}
      </span>
      <span className="flex-1 text-xs text-slate-300 break-all" title={item.url}>
        {shortRepo(item.url)}
      </span>
      <span className="text-[10px] text-slate-500 shrink-0">{item.since}s</span>
      <button
        onClick={() => onSkip(item.url)}
        title="Skip this repository"
        className="p-1 hover:bg-slate-700 rounded text-slate-500 hover:text-red-400 shrink-0"
      >
        <Ban size={13} />
      </button>
    </div>
  )
}

export default function ActivityPanel({ activity }) {
  const [progress, setProgress] = useState(null)
  const [regex, setRegex] = useState(null)
  const toast = useToast()

  useEffect(() => {
    let cancelled = false
    const load = () => {
      api.scanProgress().then((d) => !cancelled && setProgress(d)).catch(() => {})
      api.regexStats().then((d) => !cancelled && setRegex(d)).catch(() => {})
    }
    load()
    const id = setInterval(load, 2000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  const skip = (url) => {
    api
      .skipRepo(url)
      .then(() => toast(`Skip requested for ${shortRepo(url)}`, 'info'))
      .catch((e) => toast(`Skip failed: ${e.message}`, 'error'))
  }

  const a = activity || {}
  const pct = Math.max(0, Math.min(100, Math.round(progress?.progress || 0)))

  return (
    <div className="space-y-5">
      <div>
        <h3 className="text-xs uppercase tracking-wide text-slate-500 mb-2 flex items-center gap-1.5">
          <Gauge size={13} /> Scanner progress
        </h3>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mb-3">
          <Stat label="Repositories scanned" value={(progress?.repos_scanned || 0).toLocaleString()} />
          <Stat label="Files processed" value={(progress?.files_processed || 0).toLocaleString()} />
          <Stat label="Matches found" value={(progress?.matches_found || 0).toLocaleString()} color="text-orange-400" />
          <Stat label="Errors" value={(progress?.errors || 0).toLocaleString()} color="text-red-400" />
        </div>

        <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-3">
          <div className="flex items-center justify-between text-xs text-slate-400 mb-1.5">
            <span>
              {progress?.is_scanning ? 'Scanning' : 'Idle'}
              {progress?.speed ? ` · ${Math.round(progress.speed)} files/s` : ''}
              {progress?.elapsed_seconds ? ` · ${fmtDuration(progress.elapsed_seconds)} elapsed` : ''}
            </span>
            <span>
              {pct}%
              {progress?.estimated_time_remaining
                ? ` · ETA ${fmtDuration(progress.estimated_time_remaining)}`
                : ''}
            </span>
          </div>
          <div className="h-2 bg-slate-800 rounded overflow-hidden">
            <div className="h-full bg-gradient-to-r from-cyan-500 to-blue-500 transition-all" style={{ width: `${pct}%` }} />
          </div>
          {progress?.current_repo && (
            <div className="mt-2 text-[11px] text-slate-500 break-all">
              {shortRepo(progress.current_repo)}
              {progress.current_file ? ` › ${progress.current_file}` : ''}
            </div>
          )}
        </div>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-5 gap-2">
        <Stat label="Fetched" value={(a.total_fetched || 0).toLocaleString()} />
        <Stat label="Cloned" value={(a.total_cloned || 0).toLocaleString()} />
        <Stat label="Scanned" value={(a.total_scanned || 0).toLocaleString()} color="text-green-400" />
        <Stat label="Failed" value={(a.total_failed || 0).toLocaleString()} color="text-red-400" />
        <Stat label="Rate limited" value={(a.rate_limited || 0).toLocaleString()} color="text-yellow-400" />
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
        <div>
          <h3 className="text-xs uppercase tracking-wide text-slate-500 mb-2 flex items-center gap-1.5">
            <Download size={13} /> Fetching ({a.fetching?.length || 0})
          </h3>
          {(a.fetching || []).length === 0 && <div className="text-xs text-slate-600 italic">idle</div>}
          {(a.fetching || []).map((it, i) => (
            <RepoRow key={`${it.url}-${i}`} item={it} phase="fetching" onSkip={skip} />
          ))}
        </div>

        <div>
          <h3 className="text-xs uppercase tracking-wide text-slate-500 mb-2 flex items-center gap-1.5">
            <FileSearch size={13} /> Scanning ({a.scanning?.length || 0})
          </h3>
          {(a.scanning || []).length === 0 && <div className="text-xs text-slate-600 italic">idle</div>}
          {(a.scanning || []).map((it, i) => (
            <RepoRow key={`${it.url}-${i}`} item={it} phase="scanning" onSkip={skip} />
          ))}
        </div>
      </div>

      <div>
        <h3 className="text-xs uppercase tracking-wide text-slate-500 mb-2 flex items-center gap-1.5">
          <Cpu size={13} /> Regex engine
        </h3>
        <div className="grid grid-cols-3 gap-2">
          <Stat label="Patterns compiled" value={(regex?.patterns_compiled ?? 0).toLocaleString()} />
          <Stat label="Patterns total" value={(regex?.patterns_total ?? 0).toLocaleString()} />
          <Stat label="Max workers" value={(regex?.max_workers ?? 0).toLocaleString()} />
        </div>
      </div>
    </div>
  )
}
