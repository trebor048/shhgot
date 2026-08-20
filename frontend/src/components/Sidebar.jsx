import React from 'react'
import { Filter, X } from 'lucide-react'

export default function Sidebar({ isOpen, stats, filters, setFilters, onClose }) {
  const sources = Object.keys(stats.matches_by_source || {})
  const signatures = Object.keys(stats.matches_by_signature || {}).sort(
    (a, b) => (stats.matches_by_signature[b] || 0) - (stats.matches_by_signature[a] || 0)
  )
  const priorities = [3, 2, 1, 0]

  return (
    <>
      {/* Overlay for mobile */}
      {isOpen && (
        <div
          className="md:hidden fixed inset-0 bg-black/50 z-40"
          onClick={onClose}
        />
      )}

      {/* Sidebar */}
      <div
        className={`fixed md:relative md:w-72 w-80 h-screen bg-slate-900 border-r border-slate-800 overflow-y-auto transition-transform z-40 ${
          isOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0'
        }`}
      >
        <div className="sticky top-0 bg-slate-900 border-b border-slate-800 p-4 flex items-center justify-between">
          <h2 className="font-semibold text-lg flex items-center gap-2">
            <Filter size={18} />
            Filters
          </h2>
          <button
            onClick={onClose}
            className="md:hidden p-1 hover:bg-slate-800 rounded"
          >
            <X size={20} />
          </button>
        </div>

        <div className="p-4 space-y-6">
          {/* Search */}
          <div>
            <label className="block text-sm font-medium text-slate-300 mb-2">
              Search
            </label>
            <input
              type="text"
              placeholder="URL, file, signature..."
              value={filters.search}
              onChange={(e) => setFilters({ ...filters, search: e.target.value })}
              className="w-full px-3 py-2 bg-slate-800 border border-slate-700 rounded text-sm focus:outline-none focus:border-cyan-500 focus:ring-1 focus:ring-cyan-500"
            />
          </div>

          {/* Priority filter */}
          <div>
            <label className="block text-sm font-medium text-slate-300 mb-3">
              Priority
            </label>
            <div className="space-y-2">
              <button
                onClick={() => setFilters({ ...filters, priority: '' })}
                className={`w-full px-3 py-2 rounded text-sm text-left transition ${
                  filters.priority === ''
                    ? 'bg-cyan-600 text-white'
                    : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                }`}
              >
                All
              </button>
              {priorities.map(p => (
                <button
                  key={p}
                  onClick={() => setFilters({ ...filters, priority: String(p) })}
                  className={`w-full px-3 py-2 rounded text-sm text-left flex items-center justify-between transition ${
                    filters.priority === String(p)
                      ? 'bg-cyan-600 text-white'
                      : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                  }`}
                >
                  <span>
                    {p === 3 ? '🔴 Critical' : p === 2 ? '🟠 High' : p === 1 ? '🟡 Medium' : '🟢 Low'}
                  </span>
                  <span className="text-xs bg-black/30 px-2 py-1 rounded">
                    {stats.matches_by_priority?.[p] || 0}
                  </span>
                </button>
              ))}
            </div>
          </div>

          {/* Source filter */}
          {sources.length > 0 && (
            <div>
              <label className="block text-sm font-medium text-slate-300 mb-3">
                Source
              </label>
              <div className="space-y-2">
                <button
                  onClick={() => setFilters({ ...filters, source: '' })}
                  className={`w-full px-3 py-2 rounded text-sm text-left transition ${
                    filters.source === ''
                      ? 'bg-cyan-600 text-white'
                      : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                  }`}
                >
                  All
                </button>
                {sources.map(source => (
                  <button
                    key={source}
                    onClick={() => setFilters({ ...filters, source })}
                    className={`w-full px-3 py-2 rounded text-sm text-left flex items-center justify-between transition ${
                      filters.source === source
                        ? 'bg-cyan-600 text-white'
                        : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                    }`}
                  >
                    <span>{source}</span>
                    <span className="text-xs bg-black/30 px-2 py-1 rounded">
                      {stats.matches_by_source?.[source] || 0}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          )}

          {/* Signature filter */}
          {signatures.length > 0 && (
            <div>
              <label className="block text-sm font-medium text-slate-300 mb-3">
                Signature ({signatures.length})
              </label>
              <div className="space-y-1 max-h-64 overflow-y-auto">
                <button
                  onClick={() => setFilters({ ...filters, signature: '' })}
                  className={`w-full px-3 py-2 rounded text-sm text-left transition ${
                    filters.signature === ''
                      ? 'bg-cyan-600 text-white'
                      : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                  }`}
                >
                  All
                </button>
                {signatures.map(sig => (
                  <button
                    key={sig}
                    onClick={() => setFilters({ ...filters, signature: sig })}
                    className={`w-full px-3 py-2 rounded text-sm text-left flex items-center justify-between transition truncate ${
                      filters.signature === sig
                        ? 'bg-cyan-600 text-white'
                        : 'bg-slate-800 hover:bg-slate-700 text-slate-300'
                    }`}
                    title={sig}
                  >
                    <span className="truncate">{sig}</span>
                    <span className="text-xs bg-black/30 px-2 py-1 rounded flex-shrink-0 ml-2">
                      {stats.matches_by_signature?.[sig] || 0}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          )}

          {/* Clear filters */}
          <button
            onClick={() => setFilters({ source: '', signature: '', priority: '', search: '' })}
            className="w-full px-3 py-2 bg-slate-800 hover:bg-slate-700 rounded text-sm transition"
          >
            Clear all filters
          </button>
        </div>
      </div>
    </>
  )
}
