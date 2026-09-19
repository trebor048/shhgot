import React, { useState } from 'react'
import { ExternalLink, ChevronDown, Copy, Trash2, Sparkles } from 'lucide-react'

export default function MatchTable({ matches, totalMatches, filters, setFilters }) {
  const [expandedId, setExpandedId] = useState(null)

  const getPriorityColor = (priority) => {
    switch (priority) {
      case 3:
        return 'bg-red-500/20 text-red-300'
      case 2:
        return 'bg-orange-500/20 text-orange-300'
      case 1:
        return 'bg-yellow-500/20 text-yellow-300'
      default:
        return 'bg-green-500/20 text-green-300'
    }
  }

  const getPriorityLabel = (priority) => {
    switch (priority) {
      case 3:
        return '🔴 Critical'
      case 2:
        return '🟠 High'
      case 1:
        return '🟡 Medium'
      default:
        return '🟢 Low'
    }
  }

  const copyToClipboard = (text) => {
    navigator.clipboard.writeText(text)
  }

  const deleteMatch = (matchId) => {
    // TODO: Add API call to delete match
    console.log('Delete match:', matchId)
  }

  const analyzeWithAI = (match) => {
    // TODO: Add AI analysis functionality
    console.log('Analyze with AI:', match)
  }

  const toggleExpanded = (matchId) => {
    setExpandedId(expandedId === matchId ? null : matchId)
  }

  const getRepoName = (url) => {
    return url.split('/').pop()?.replace('.git', '') || url
  }

  const truncate = (text, length = 50) => {
    return text.length > length ? text.substring(0, length) + '...' : text
  }

  return (
    <div className="p-6">
      {/* Stats bar */}
      <div className="mb-6 p-4 bg-slate-800/50 rounded-lg border border-slate-700">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm text-slate-400">Showing matches</p>
            <p className="text-2xl font-bold text-cyan-400">
              {matches.length.toLocaleString()}
              <span className="text-sm text-slate-400 ml-2">of {totalMatches.toLocaleString()} total</span>
            </p>
          </div>
          {Object.values(filters).some(f => f) && (
            <button
              onClick={() => setFilters({ source: '', signature: '', priority: '', search: '' })}
              className="px-4 py-2 bg-slate-700 hover:bg-slate-600 rounded text-sm transition"
            >
              Clear filters
            </button>
          )}
        </div>
      </div>

      {/* Table */}
      <div className="overflow-x-auto border border-slate-700 rounded-lg">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-slate-700 bg-slate-800/50">
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Time</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Priority</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Source</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Repository</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Signature</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">File</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">⭐</th>
              <th className="px-4 py-3 text-left font-semibold text-slate-300">Actions</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-700">
            {matches.length === 0 ? (
              <tr>
                <td colSpan="8" className="px-4 py-8 text-center text-slate-400">
                  No matches found. Waiting for scan results...
                </td>
              </tr>
            ) : (
              matches.map((match) => (
                <React.Fragment key={match.id}>
                  <tr className="hover:bg-slate-800/50 transition">
                    <td className="px-4 py-3 text-slate-300">
                      {new Date(match.timestamp).toLocaleTimeString()}
                    </td>
                    <td className="px-4 py-3">
                      <span className={`px-2 py-1 rounded text-xs font-medium ${getPriorityColor(match.priority)}`}>
                        {getPriorityLabel(match.priority)}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <span className="px-2 py-1 bg-slate-700 rounded text-xs">{match.source}</span>
                    </td>
                    <td className="px-4 py-3">
                      <a
                        href={match.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-cyan-400 hover:text-cyan-300 flex items-center gap-1 truncate"
                        title={getRepoName(match.url)}
                      >
                        {truncate(getRepoName(match.url), 30)}
                        <ExternalLink size={14} />
                      </a>
                    </td>
                    <td className="px-4 py-3">
                      <span
                        className="text-yellow-300 cursor-pointer hover:text-yellow-200 truncate block"
                        title={match.signature}
                      >
                        {truncate(match.signature, 25)}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <code className="bg-slate-800 px-2 py-1 rounded text-xs text-orange-300 truncate block">
                        {truncate(match.file, 35)}
                      </code>
                    </td>
                    <td className="px-4 py-3 text-slate-300">
                      {match.stars > 0 ? match.stars.toLocaleString() : '-'}
                    </td>
                    <td className="px-4 py-3">
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          toggleExpanded(match.id)
                        }}
                        className="p-1 hover:bg-slate-700 rounded transition"
                        title={expandedId === match.id ? "Hide details" : "View details"}
                      >
                        <ChevronDown size={18} className={`transition-transform ${expandedId === match.id ? 'rotate-180' : ''}`} />
                      </button>
                    </td>
                  </tr>
                  {expandedId === match.id && (
                    <tr className="bg-slate-800/30 border-slate-700">
                      <td colSpan="8" className="px-4 py-4">
                        <div className="space-y-4">
                          {/* Repository Information */}
                          <div>
                            <p className="text-xs text-slate-400 uppercase tracking-wide mb-1">Repository URL</p>
                            <div className="flex items-center gap-2">
                              <code className="flex-1 bg-slate-900 px-3 py-2 rounded text-xs break-all">
                                {match.url}
                              </code>
                              <button
                                onClick={() => copyToClipboard(match.url)}
                                className="p-2 hover:bg-slate-700 rounded transition"
                                title="Copy URL"
                              >
                                <Copy size={16} />
                              </button>
                            </div>
                          </div>
                          
                          {/* File Path */}
                          <div>
                            <p className="text-xs text-slate-400 uppercase tracking-wide mb-1">File Path</p>
                            <div className="flex items-center gap-2">
                              <code className="flex-1 bg-slate-900 px-3 py-2 rounded text-xs break-all">
                                {match.file}
                              </code>
                              <button
                                onClick={() => copyToClipboard(match.file)}
                                className="p-2 hover:bg-slate-700 rounded transition"
                                title="Copy file path"
                              >
                                <Copy size={16} />
                              </button>
                            </div>
                          </div>
                          
                          {/* Detected Secrets */}
                          <div>
                            <p className="text-xs text-slate-400 uppercase tracking-wide mb-2">
                              Detected Secrets ({match.matches.length})
                            </p>
                            <div className="space-y-3">
                              {match.matches.map((m, i) => (
                                <div key={i} className="bg-slate-900/50 rounded-lg p-3 border border-slate-700">
                                  <div className="flex items-start justify-between mb-2">
                                    <span className="text-xs text-slate-400">Match #{i + 1}</span>
                                    <div className="flex gap-1">
                                      <button
                                        onClick={() => copyToClipboard(m)}
                                        className="p-1 hover:bg-slate-700 rounded transition text-slate-400 hover:text-slate-200"
                                        title="Copy secret"
                                      >
                                        <Copy size={14} />
                                      </button>
                                      <button
                                        onClick={() => analyzeWithAI(match)}
                                        className="p-1 hover:bg-blue-600 rounded transition text-blue-400 hover:text-blue-300"
                                        title="Analyze with AI"
                                      >
                                        <Sparkles size={14} />
                                      </button>
                                      <button
                                        onClick={() => deleteMatch(match.id)}
                                        className="p-1 hover:bg-red-600 rounded transition text-red-400 hover:text-red-300"
                                        title="Delete match"
                                      >
                                        <Trash2 size={14} />
                                      </button>
                                    </div>
                                  </div>
                                  <code className="block bg-slate-900 px-3 py-2 rounded text-xs break-all text-green-300 font-mono">
                                    {m.length > 100 ? (
                                      <>
                                        {m.substring(0, 30)}
                                        <span className="text-slate-500">{'*'.repeat(Math.max(0, m.length - 60))}</span>
                                        {m.substring(Math.max(0, m.length - 30))}
                                      </>
                                    ) : (
                                      <>
                                        {m.substring(0, 8)}
                                        <span className="text-slate-500">{'*'.repeat(Math.max(0, m.length - 16))}</span>
                                        {m.substring(Math.max(0, m.length - 8))}
                                      </>
                                    )}
                                  </code>
                                </div>
                              ))}
                            </div>
                          </div>
                          
                          {/* Match Metadata */}
                          <div className="grid grid-cols-3 gap-4 pt-3 border-t border-slate-700">
                            <div>
                              <p className="text-xs text-slate-400 mb-1">Detected</p>
                              <p className="text-sm font-medium text-slate-300">{new Date(match.timestamp).toLocaleString()}</p>
                            </div>
                            <div>
                              <p className="text-xs text-slate-400 mb-1">Priority</p>
                              <span className={`inline-flex px-2 py-1 rounded text-xs font-medium ${getPriorityColor(match.priority)}`}>
                                {getPriorityLabel(match.priority)}
                              </span>
                            </div>
                            <div>
                              <p className="text-xs text-slate-400 mb-1">Repository Stars</p>
                              <p className="text-sm font-medium text-slate-300">{match.stars > 0 ? match.stars.toLocaleString() : 'N/A'}</p>
                            </div>
                          </div>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
