import React, { useState, useEffect, useCallback } from 'react'
import Dashboard from './components/Dashboard'
import Sidebar from './components/Sidebar'
import MatchTable from './components/MatchTable'
import StatsPanel from './components/StatsPanel'
import ScanProgress from './components/ScanProgress'
import { AlertCircle, Menu, X } from 'lucide-react'

export default function App() {
  const [matches, setMatches] = useState([])
  const [isScanning, setIsScanning] = useState(false)
  const [stats, setStats] = useState({
    total_matches: 0,
    matches_by_source: {},
    matches_by_signature: {},
    matches_by_priority: {},
    top_signatures: [],
  })
  const [filters, setFilters] = useState({
    source: '',
    signature: '',
    priority: '',
    search: '',
  })
  const [connected, setConnected] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(true)
  const [activeTab, setActiveTab] = useState('matches')

  // Polling for updates - preserve existing matches and merge with new ones
  useEffect(() => {
    const pollInterval = setInterval(async () => {
      try {
        const response = await fetch('/api/matches?page=1&limit=1000')
        const data = await response.json()
        
        // Merge new matches with existing ones (avoid duplicates)
        setMatches(prevMatches => {
          const newMatches = data.matches || []
          const existingIds = new Set(prevMatches.map(m => m.id))
          const uniqueNew = newMatches.filter(m => !existingIds.has(m.id))
          return [...prevMatches, ...uniqueNew]
        })
        
        if (data.stats) {
          setStats(data.stats)
        }
        setConnected(true)
      } catch (err) {
        console.error('Polling error:', err)
        setConnected(false)
      }
    }, 5000) // Increased to 5 seconds to reduce API calls

    return () => clearInterval(pollInterval)
  }, [])

  const filteredMatches = matches.filter(match => {
    if (filters.source && match.source !== filters.source) return false
    if (filters.signature && match.signature !== filters.signature) return false
    if (filters.priority && match.priority !== parseInt(filters.priority)) return false
    if (filters.search) {
      const search = filters.search.toLowerCase()
      return (
        match.url.toLowerCase().includes(search) ||
        match.file.toLowerCase().includes(search) ||
        match.signature.toLowerCase().includes(search)
      )
    }
    return true
  })

  const exportMatches = () => {
    const csv = [
      ['Timestamp', 'Source', 'URL', 'File', 'Signature', 'Matches', 'Stars', 'Priority'],
      ...filteredMatches.map(m => [
        new Date(m.timestamp).toISOString(),
        m.source,
        m.url,
        m.file,
        m.signature,
        m.matches.join(';'),
        m.stars,
        m.priority,
      ])
    ]
    
    const csvContent = csv.map(row => 
      row.map(cell => `"${String(cell).replace(/"/g, '""')}"`).join(',')
    ).join('\n')
    
    const blob = new Blob([csvContent], { type: 'text/csv' })
    const url = window.URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `shhgit-matches-${new Date().toISOString().split('T')[0]}.csv`
    a.click()
    window.URL.revokeObjectURL(url)
  }

  return (
    <div className="flex h-screen bg-slate-950 text-slate-100">
      {/* Scan Progress Indicator */}
      <ScanProgress isScanning={isScanning} />

      {/* Mobile sidebar toggle */}
      <button
        onClick={() => setSidebarOpen(!sidebarOpen)}
        className="md:hidden fixed bottom-4 right-4 z-50 p-2 bg-cyan-600 rounded-lg hover:bg-cyan-700"
      >
        {sidebarOpen ? <X size={24} /> : <Menu size={24} />}
      </button>

      {/* Sidebar */}
      <Sidebar
        isOpen={sidebarOpen}
        stats={stats}
        filters={filters}
        setFilters={setFilters}
        onClose={() => setSidebarOpen(false)}
      />

      {/* Main content */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Header */}
        <header className="border-b border-slate-800 bg-slate-900/50 backdrop-blur">
          <div className="px-6 py-4 flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="text-2xl font-bold bg-gradient-to-r from-cyan-400 to-blue-500 bg-clip-text text-transparent">
                shhgit
              </div>
              <span className="text-xs px-2 py-1 bg-cyan-500/20 text-cyan-300 rounded">
                Real-time Scanner
              </span>
            </div>
            
            <div className="flex items-center gap-4">
              <div className={`flex items-center gap-2 px-3 py-1 rounded text-sm ${
                connected
                  ? 'bg-green-500/20 text-green-300'
                  : 'bg-red-500/20 text-red-300'
              }`}>
                <div className={`w-2 h-2 rounded-full ${connected ? 'bg-green-400' : 'bg-red-400'} animate-pulse`} />
                {connected ? 'Connected' : 'Disconnected'}
              </div>
              
              <button
                onClick={exportMatches}
                className="px-3 py-1 bg-slate-800 hover:bg-slate-700 rounded text-sm transition"
              >
                Export CSV
              </button>
            </div>
          </div>

          {/* Tab navigation */}
          <div className="flex border-t border-slate-800 px-6">
            {[
              { id: 'matches', label: 'Matches', icon: '◆' },
              { id: 'stats', label: 'Statistics', icon: '📊' },
              { id: 'dashboard', label: 'Dashboard', icon: '📈' },
            ].map(tab => (
              <button
                key={tab.id}
                onClick={() => setActiveTab(tab.id)}
                className={`px-4 py-3 border-b-2 transition ${
                  activeTab === tab.id
                    ? 'border-cyan-500 text-cyan-400'
                    : 'border-transparent text-slate-400 hover:text-slate-300'
                }`}
              >
                <span className="mr-2">{tab.icon}</span>
                {tab.label}
              </button>
            ))}
          </div>
        </header>

        {/* Content area */}
        <main className="flex-1 overflow-auto">
          {!connected && (
            <div className="bg-yellow-500/10 border border-yellow-500/30 text-yellow-200 px-6 py-3 flex items-center gap-3">
              <AlertCircle size={20} />
              Waiting for server connection...
            </div>
          )}

          {activeTab === 'matches' && (
            <MatchTable
              matches={filteredMatches}
              totalMatches={stats.total_matches}
              filters={filters}
              setFilters={setFilters}
            />
          )}
          
          {activeTab === 'stats' && (
            <StatsPanel stats={stats} />
          )}
          
          {activeTab === 'dashboard' && (
            <Dashboard matches={matches} stats={stats} />
          )}
        </main>
      </div>
    </div>
  )
}
