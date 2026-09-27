import React, { useMemo } from 'react'
import { Filter, Search, X } from 'lucide-react'
import { priorityMeta } from '../lib/format'

const PRIORITIES = [3, 2, 1, 0]

export default function Sidebar({ isOpen, onClose, stats, filters, setFilters }) {
  const sources = useMemo(
    () => Object.keys(stats.matches_by_source || {}).sort(
      (a, b) => (stats.matches_by_source[b] || 0) - (stats.matches_by_source[a] || 0)
    ),
    [stats],
  )
  const signatures = useMemo(
    () => Object.keys(stats.matches_by_signature || {}).sort(
      (a, b) => (stats.matches_by_signature[b] || 0) - (stats.matches_by_signature[a] || 0)
    ),
    [stats],
  )

  const set = (patch) => setFilters((f) => ({ ...f, ...patch }))
  const active =
    filters.search || filters.source || filters.signature || filters.priority !== ''

  return (
    <>
      {isOpen && (
        <div className="lg:hidden fixed inset-0 bg-black/60 z-30" onClick={onClose} />
      )}

      <aside
        className={`fixed lg:static inset-y-0 left-0 z-40 w-72 shrink-0 bg-slate-900/95 lg:bg-slate-900/40 border-r border-slate-800 overflow-y-auto transition-transform ${
          isOpen ? 'translate-x-0' : '-translate-x-full lg:translate-x-0'
        }`}
      >
        <div className="sticky top-0 bg-slate-900/95 backdrop-blur border-b border-slate-800 px-4 py-3 flex items-center justify-between">
          <h2 className="font-semibold flex items-center gap-2">
            <Filter size={16} />
            Filters
          </h2>
          <div className="flex items-center gap-2">
            {active && (
              <button
                onClick={() => set({ search: '', source: '', signature: '', priority: '' })}
                className="text-xs text-cyan-400 hover:text-cyan-300"
              >
                clear
              </button>
            )}
            <button onClick={onClose} className="lg:hidden p-1 hover:bg-slate-800 rounded">
              <X size={18} />
            </button>
          </div>
        </div>

        <div className="p-4 space-y-5">
          <div>
            <label className="block text-xs uppercase tracking-wide text-slate-500 mb-1.5">
              Search
            </label>
            <div className="relative">
              <Search size={14} className="absolute left-2.5 top-2.5 text-slate-500" />
              <input
                value={filters.search}
                onChange={(e) => set({ search: e.target.value })}
                placeholder="url, file, signature, secret"
                className="w-full pl-8 pr-2 py-1.5 bg-slate-950 border border-slate-700 rounded text-sm focus:outline-none focus:border-cyan-500"
              />
            </div>
          </div>

          <div>
            <label className="block text-xs uppercase tracking-wide text-slate-500 mb-1.5">
              Priority
            </label>
            <div className="grid grid-cols-1 gap-1">
              <button
                onClick={() => set({ priority: '' })}
                className={`text-left px-2 py-1.5 rounded text-sm transition ${
                  filters.priority === ''
                    ? 'bg-cyan-600 text-white'
                    : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                }`}
              >
                All priorities
              </button>
              {PRIORITIES.map((p) => {
                const m = priorityMeta(p)
                return (
                  <button
                    key={p}
                    onClick={() => set({ priority: String(p) })}
                    className={`flex items-center justify-between px-2 py-1.5 rounded text-sm transition ${
                      filters.priority === String(p)
                        ? 'bg-cyan-600 text-white'
                        : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                    }`}
                  >
                    <span className="flex items-center gap-2">
                      <span className={`w-2 h-2 rounded-full ${m.dot}`} />
                      {m.label}
                    </span>
                    <span className="text-[10px] bg-black/30 px-1.5 py-0.5 rounded">
                      {stats.matches_by_priority?.[p] || 0}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>

          {sources.length > 0 && (
            <div>
              <label className="block text-xs uppercase tracking-wide text-slate-500 mb-1.5">
                Source
              </label>
              <select
                value={filters.source}
                onChange={(e) => set({ source: e.target.value })}
                className="w-full px-2 py-1.5 bg-slate-950 border border-slate-700 rounded text-sm focus:outline-none focus:border-cyan-500"
              >
                <option value="">All sources</option>
                {sources.map((s) => (
                  <option key={s} value={s}>
                    {s} ({stats.matches_by_source?.[s] || 0})
                  </option>
                ))}
              </select>
            </div>
          )}

          <div>
            <label className="block text-xs uppercase tracking-wide text-slate-500 mb-1.5">
              Signature {signatures.length > 0 && `(${signatures.length})`}
            </label>
            <div className="max-h-72 overflow-y-auto rounded border border-slate-800 divide-y divide-slate-800">
              <button
                onClick={() => set({ signature: '' })}
                className={`w-full text-left px-2 py-1.5 text-sm ${
                  filters.signature === '' ? 'bg-slate-800 text-cyan-300' : 'hover:bg-slate-800/60 text-slate-300'
                }`}
              >
                All signatures
              </button>
              {signatures.map((s) => (
                <button
                  key={s}
                  onClick={() => set({ signature: s })}
                  title={s}
                  className={`w-full flex items-center justify-between gap-2 px-2 py-1.5 text-sm ${
                    filters.signature === s
                      ? 'bg-slate-800 text-cyan-300'
                      : 'hover:bg-slate-800/60 text-slate-300'
                  }`}
                >
                  <span className="truncate">{s}</span>
                  <span className="text-[10px] text-slate-500 shrink-0">
                    {stats.matches_by_signature?.[s] || 0}
                  </span>
                </button>
              ))}
            </div>
          </div>
        </div>
      </aside>
    </>
  )
}
