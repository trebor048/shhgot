import React, { useState, useEffect } from 'react'
import { Activity, CheckCircle, AlertCircle, Zap } from 'lucide-react'

export default function ScanProgress({ isScanning }) {
  const [progress, setProgress] = useState(0)
  const [scanStats, setScanStats] = useState(null)
  const [scanHistory, setScanHistory] = useState([])

  // Fetch scan progress
  useEffect(() => {
    if (!isScanning) return

    const interval = setInterval(async () => {
      try {
        const response = await fetch('/api/scan/progress')
        const data = await response.json()
        setProgress(data.progress || 0)
        setScanStats(data)
      } catch (err) {
        console.error('Failed to fetch scan progress:', err)
      }
    }, 500) // Update every 500ms

    return () => clearInterval(interval)
  }, [isScanning])

  if (!isScanning && progress === 0) {
    return null
  }

  const getProgressColor = () => {
    if (progress < 33) return 'from-blue-500 to-cyan-500'
    if (progress < 66) return 'from-yellow-500 to-orange-500'
    return 'from-green-500 to-emerald-500'
  }

  const getProgressIcon = () => {
    if (progress === 100) return <CheckCircle size={24} className="text-green-400" />
    if (progress >= 75) return <CheckCircle size={24} className="text-cyan-400" />
    return <Activity size={24} className="text-cyan-400 animate-spin" />
  }

  return (
    <div className="fixed bottom-6 right-6 z-40 w-96 bg-slate-900 border border-slate-700 rounded-lg shadow-2xl p-6">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          {getProgressIcon()}
          <h3 className="font-semibold text-slate-100">
            {progress === 100 ? 'Scan Complete' : 'Scanning...'}
          </h3>
        </div>
        {progress === 100 && (
          <span className="text-xs bg-green-500/20 text-green-300 px-2 py-1 rounded">
            Done
          </span>
        )}
      </div>

      {/* Progress Bar */}
      <div className="mb-4">
        <div className="flex justify-between items-center mb-2">
          <span className="text-xs text-slate-400">Progress</span>
          <span className="text-sm font-bold text-cyan-400">{progress}%</span>
        </div>
        <div className="w-full bg-slate-800 rounded-full h-2 overflow-hidden">
          <div
            className={`h-full bg-gradient-to-r ${getProgressColor()} transition-all duration-300`}
            style={{ width: `${progress}%` }}
          />
        </div>
      </div>

      {/* Stats Grid */}
      {scanStats && (
        <div className="grid grid-cols-2 gap-3 mb-4">
          <div className="bg-slate-800/50 rounded p-2">
            <p className="text-xs text-slate-400 mb-1">Repositories Scanned</p>
            <p className="text-lg font-bold text-cyan-400">
              {scanStats.repos_scanned || 0}
            </p>
          </div>
          <div className="bg-slate-800/50 rounded p-2">
            <p className="text-xs text-slate-400 mb-1">Files Processed</p>
            <p className="text-lg font-bold text-orange-400">
              {scanStats.files_processed || 0}
            </p>
          </div>
          <div className="bg-slate-800/50 rounded p-2">
            <p className="text-xs text-slate-400 mb-1">Matches Found</p>
            <p className="text-lg font-bold text-red-400">
              {scanStats.matches_found || 0}
            </p>
          </div>
          <div className="bg-slate-800/50 rounded p-2">
            <p className="text-xs text-slate-400 mb-1">Speed (files/s)</p>
            <p className="text-lg font-bold text-green-400">
              {(scanStats.speed || 0).toFixed(1)}
            </p>
          </div>
        </div>
      )}

      {/* Details */}
      {scanStats && (
        <div className="space-y-2 mb-4 pb-4 border-b border-slate-700">
          <div className="flex items-center gap-2 text-xs text-slate-400">
            <Zap size={14} className="text-yellow-400" />
            <span>Current: {scanStats.current_repo || 'Initializing...'}</span>
          </div>

          {scanStats.estimated_time_remaining && (
            <div className="flex items-center gap-2 text-xs text-slate-400">
              <Activity size={14} className="text-cyan-400" />
              <span>ETA: {scanStats.estimated_time_remaining}s remaining</span>
            </div>
          )}

          <div className="flex items-center gap-2 text-xs text-slate-400">
            <AlertCircle size={14} className="text-orange-400" />
            <span>Regex Cache Hit: {(scanStats.cache_hit_rate || 0).toFixed(1)}%</span>
          </div>
        </div>
      )}

      {/* Status Message */}
      {progress === 100 && (
        <div className="bg-green-500/10 border border-green-500/20 rounded p-3 text-sm text-green-300">
          ✓ Scan completed successfully. Check the matches tab for results.
        </div>
      )}
    </div>
  )
}
