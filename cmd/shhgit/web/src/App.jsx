import React, { useMemo, useState } from 'react'
import Header from './components/Header'
import Sidebar from './components/Sidebar'
import MatchTable from './components/MatchTable'
import StatsPanel from './components/StatsPanel'
import DashboardPanel from './components/DashboardPanel'
import LogsPanel from './components/LogsPanel'
import TokensPanel from './components/TokensPanel'
import ActivityPanel from './components/ActivityPanel'
import ReviewPanel from './components/ReviewPanel'
import SettingsPanel from './components/SettingsPanel'
import FileModal from './components/FileModal'
import Particles from './components/Particles'
import { ToastProvider, useToast } from './components/Toast'
import { useFeed } from './lib/useFeed'
import { csvEscape } from './lib/format'
import { AlertCircle } from 'lucide-react'

function Workspace() {
  const toast = useToast()
  const feed = useFeed()
  const [activeTab, setActiveTab] = useState('matches')
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [filters, setFilters] = useState({ search: '', source: '', signature: '', priority: '' })
  const [fileMatch, setFileMatch] = useState(null)
  const [pendingReview, setPendingReview] = useState(null)
  const [paused, setPaused] = useState(false)
  const [frozen, setFrozen] = useState([])

  // Pause freezes the rendered list (over a snapshot) while the live feed keeps
  // buffering in the background, so an operator can read a hit without the
  // table shifting underneath them.
  const sourceMatches = paused ? frozen : feed.matches

  const togglePause = () => {
    if (paused) {
      setPaused(false)
    } else {
      setFrozen(feed.matches)
      setPaused(true)
    }
  }

  const filtered = useMemo(() => {
    const q = filters.search.trim().toLowerCase()
    return sourceMatches.filter((m) => {
      if (filters.priority !== '' && m.priority !== parseInt(filters.priority, 10)) return false
      if (filters.source && m.source !== filters.source) return false
      if (filters.signature && m.signature !== filters.signature) return false
      if (q) {
        const hay = `${m.url || ''} ${m.file || ''} ${m.signature || ''} ${(m.matches || []).join(' ')}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [sourceMatches, filters])

  const counts = {
    matches: feed.stats.total_matches || feed.matches.length,
    tokens: feed.tokens.length,
    logs: feed.logs.length,
    activity: (feed.activity?.fetching?.length || 0) + (feed.activity?.scanning?.length || 0),
  }

  const exportCsv = () => {
    const header = ['Timestamp', 'Source', 'URL', 'File', 'Signature', 'Matches', 'Stars', 'Priority']
    const rows = filtered.map((m) => [
      m.timestamp ? new Date(m.timestamp).toISOString() : '',
      m.source,
      m.url,
      m.file,
      m.signature,
      (m.matches || []).join(';'),
      m.stars,
      m.priority,
    ])
    const csv = [header, ...rows].map((r) => r.map(csvEscape).join(',')).join('\n')
    const blob = new Blob([csv], { type: 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `shhgit-matches-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
    toast(`Exported ${rows.length.toLocaleString()} matches`, 'ok')
  }

  return (
    <div className="relative z-10 flex h-screen">
      <Particles />
      <Sidebar
        isOpen={sidebarOpen}
        onClose={() => setSidebarOpen(false)}
        stats={feed.stats}
        filters={filters}
        setFilters={setFilters}
      />

      <div className="flex-1 flex flex-col min-w-0">
        <Header
          activeTab={activeTab}
          setActiveTab={(t) => {
            setActiveTab(t)
            setSidebarOpen(false)
          }}
          connected={feed.connected}
          counts={counts}
          sidebarOpen={sidebarOpen}
          setSidebarOpen={setSidebarOpen}
          onExport={exportCsv}
          canExport={filtered.length > 0}
        />

        {!feed.connected && (
          <div className="flex items-center gap-2 px-4 py-2 text-xs text-yellow-200 bg-yellow-500/10 border-b border-yellow-500/30">
            <AlertCircle size={14} />
            Reconnecting to the live feed… results may be stale.
          </div>
        )}

        <main className="flex-1 overflow-y-auto p-4 min-h-0">
          {activeTab === 'matches' && (
            <MatchTable
              matches={filtered}
              totalMatches={feed.stats.total_matches || feed.matches.length}
              filters={filters}
              setFilters={setFilters}
              paused={paused}
              onTogglePause={togglePause}
              onViewFile={setFileMatch}
              onReview={(m) => {
                setPendingReview(m)
                setActiveTab('review')
              }}
            />
          )}
          {activeTab === 'review' && (
            <ReviewPanel
              pendingMatch={pendingReview}
              onConsumePending={() => setPendingReview(null)}
              reviewTick={feed.reviewTick}
            />
          )}
          {activeTab === 'tokens' && <TokensPanel tokens={feed.tokens} />}
          {activeTab === 'activity' && <ActivityPanel activity={feed.activity} />}
          {activeTab === 'stats' && <StatsPanel stats={feed.stats} />}
          {activeTab === 'dashboard' && <DashboardPanel matches={feed.matches} stats={feed.stats} />}
          {activeTab === 'logs' && <LogsPanel logs={feed.logs} />}
          {activeTab === 'settings' && <SettingsPanel />}
        </main>
      </div>

      {fileMatch && <FileModal match={fileMatch} onClose={() => setFileMatch(null)} />}
    </div>
  )
}

export default function App() {
  return (
    <ToastProvider>
      <Workspace />
    </ToastProvider>
  )
}
