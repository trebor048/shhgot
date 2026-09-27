import React from 'react'
import { Download, Menu, ShieldAlert, X } from 'lucide-react'

const TABS = [
  { id: 'matches', label: 'Matches' },
  { id: 'review', label: 'Review' },
  { id: 'tokens', label: 'Tokens' },
  { id: 'activity', label: 'Activity' },
  { id: 'stats', label: 'Stats' },
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'logs', label: 'Logs' },
  { id: 'settings', label: 'Settings' },
]

export default function Header({
  activeTab,
  setActiveTab,
  connected,
  counts,
  sidebarOpen,
  setSidebarOpen,
  onExport,
  canExport,
}) {
  return (
    <header className="border-b border-slate-800 bg-slate-900/60 backdrop-blur sticky top-0 z-30">
      <div className="px-4 sm:px-6 py-3 flex items-center gap-3">
        <button
          onClick={() => setSidebarOpen(!sidebarOpen)}
          className="lg:hidden p-2 -ml-2 hover:bg-slate-800 rounded"
          aria-label="Toggle filters"
        >
          {sidebarOpen ? <X size={20} /> : <Menu size={20} />}
        </button>

        <div className="flex items-center gap-2">
          <ShieldAlert size={20} className="text-cyan-400" />
          <span className="text-xl font-bold bg-gradient-to-r from-cyan-400 to-blue-500 bg-clip-text text-transparent">
            shhgit
          </span>
          <span className="hidden sm:inline text-xs px-2 py-0.5 bg-cyan-500/15 text-cyan-300 rounded border border-cyan-500/30">
            live secret scanner
          </span>
        </div>

        <div className="flex-1" />

        <a
          href="?legacy=1"
          className="hidden md:inline text-xs text-slate-400 hover:text-cyan-300 underline"
          title="Fall back to the original embedded dashboard"
        >
          legacy UI
        </a>

        {canExport && (
          <button
            onClick={onExport}
            className="flex items-center gap-1.5 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 rounded text-sm transition"
          >
            <Download size={14} />
            <span className="hidden sm:inline">CSV</span>
          </button>
        )}

        <div
          className={`flex items-center gap-2 px-3 py-1.5 rounded text-xs sm:text-sm ${
            connected ? 'bg-green-500/15 text-green-300' : 'bg-red-500/15 text-red-300'
          }`}
        >
          <span
            className={`w-2 h-2 rounded-full ${
              connected ? 'bg-green-400 animate-pulse' : 'bg-red-400'
            }`}
          />
          <span className="hidden sm:inline">{connected ? 'Connected' : 'Reconnecting'}</span>
        </div>
      </div>

      <nav className="flex gap-1 px-2 sm:px-4 overflow-x-auto border-t border-slate-800">
        {TABS.map((t) => {
          const n = counts[t.id]
          return (
            <button
              key={t.id}
              onClick={() => setActiveTab(t.id)}
              className={`px-3 py-2 text-sm whitespace-nowrap border-b-2 transition ${
                activeTab === t.id
                  ? 'border-cyan-500 text-cyan-400'
                  : 'border-transparent text-slate-400 hover:text-slate-200'
              }`}
            >
              {t.label}
              {n != null && n > 0 && (
                <span className="ml-1.5 text-[10px] px-1.5 py-0.5 bg-slate-800 rounded text-cyan-300">
                  {n}
                </span>
              )}
            </button>
          )
        })}
      </nav>
    </header>
  )
}
