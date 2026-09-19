import React, { useState, useEffect } from 'react'
import { Play, Pause, BarChart3, Zap } from 'lucide-react'

export default function PoolManager() {
  const [poolStats, setPoolStats] = useState(null)
  const [isPaused, setIsPaused] = useState(false)
  const [isLoading, setIsLoading] = useState(false)

  // Fetch pool stats
  useEffect(() => {
    const interval = setInterval(async () => {
      try {
        const response = await fetch('/api/pool/stats')
        const data = await response.json()
        setPoolStats(data)
        setIsPaused(data.is_paused)
      } catch (err) {
        console.error('Failed to fetch pool stats:', err)
      }
    }, 1000) // Update every second

    return () => clearInterval(interval)
  }, [])

  const togglePause = async () => {
    setIsLoading(true)
    try {
      const endpoint = isPaused ? '/api/pool/resume' : '/api/pool/pause'
      await fetch(endpoint, { method: 'POST' })
      setIsPaused(!isPaused)
    } catch (err) {
      console.error('Failed to toggle pause:', err)
    } finally {
      setIsLoading(false)
    }
  }

  if (!poolStats) {
    return null
  }

  const successRate = poolStats.success_rate || 0
  const utilization = Math.min(100, (poolStats.active_jobs / poolStats.workers) * 100)

  return (
    <div className="fixed top-6 right-6 w-96 bg-slate-900 border border-slate-700 rounded-lg shadow-2xl p-6 z-40">
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-2">
          <BarChart3 size={20} className="text-purple-400" />
          <h3 className="font-semibold text-slate-100">Worker Pool</h3>
        </div>
        <button
          onClick={togglePause}
          disabled={isLoading}
          className={`p-2 rounded transition ${
            isPaused
              ? 'bg-orange-500/20 text-orange-300 hover:bg-orange-500/30'
              : 'bg-green-500/20 text-green-300 hover:bg-green-500/30'
          }`}
          title={isPaused ? 'Resume' : 'Pause'}
        >
          {isPaused ? <Play size={18} /> : <Pause size={18} />}
        </button>
      </div>

      {/* Status bar */}
      <div className="mb-4 p-2 bg-slate-800/50 rounded">
        <div className="flex items-center justify-between mb-1">
          <span className="text-xs text-slate-400">Pool Status</span>
          <span className={`text-xs font-bold ${
            isPaused ? 'text-orange-400' : 'text-green-400'
          }`}>
            {isPaused ? 'PAUSED' : 'RUNNING'}
          </span>
        </div>
        <div className="w-full bg-slate-700 rounded h-1.5">
          <div
            className={`h-1.5 rounded transition-all ${
              isPaused ? 'bg-orange-500' : 'bg-green-500'
            }`}
            style={{ width: `${isPaused ? 100 : 50}%` }}
          />
        </div>
      </div>

      {/* Worker utilization */}
      <div className="grid grid-cols-2 gap-3 mb-4">
        <div className="bg-slate-800/50 p-3 rounded">
          <p className="text-xs text-slate-400 mb-1">Workers</p>
          <p className="text-lg font-bold text-cyan-400">
            {poolStats.active_jobs}/{poolStats.workers}
          </p>
          <div className="w-full bg-slate-700 rounded h-1 mt-2">
            <div
              className="h-1 rounded bg-cyan-500 transition-all"
              style={{ width: `${utilization}%` }}
            />
          </div>
        </div>

        <div className="bg-slate-800/50 p-3 rounded">
          <p className="text-xs text-slate-400 mb-1">Success Rate</p>
          <p className="text-lg font-bold text-green-400">
            {successRate.toFixed(1)}%
          </p>
          <div className="w-full bg-slate-700 rounded h-1 mt-2">
            <div
              className="h-1 rounded bg-green-500 transition-all"
              style={{ width: `${successRate}%` }}
            />
          </div>
        </div>
      </div>

      {/* Queue stats */}
      <div className="grid grid-cols-3 gap-2 mb-4">
        <div className="bg-slate-800/50 p-2 rounded text-center">
          <p className="text-xs text-slate-400">Queue</p>
          <p className="text-sm font-bold text-orange-400">{poolStats.queue_size}</p>
        </div>
        <div className="bg-slate-800/50 p-2 rounded text-center">
          <p className="text-xs text-slate-400">Processed</p>
          <p className="text-sm font-bold text-blue-400">{poolStats.jobs_processed}</p>
        </div>
        <div className="bg-slate-800/50 p-2 rounded text-center">
          <p className="text-xs text-slate-400">Failed</p>
          <p className="text-sm font-bold text-red-400">{poolStats.jobs_failed}</p>
        </div>
      </div>

      {/* Performance metrics */}
      <div className="space-y-2 text-xs border-t border-slate-700 pt-3">
        <div className="flex justify-between text-slate-400">
          <span>Avg Job Duration:</span>
          <span className="text-cyan-300 font-mono">{poolStats.avg_job_duration}</span>
        </div>
        <div className="flex justify-between text-slate-400">
          <span>Total Duration:</span>
          <span className="text-cyan-300 font-mono">{poolStats.total_duration}</span>
        </div>
        <div className="flex justify-between text-slate-400">
          <span>Busy:</span>
          <span className={poolStats.is_busy ? 'text-red-400' : 'text-green-400'}>
            {poolStats.is_busy ? 'Yes' : 'No'}
          </span>
        </div>
      </div>

      {/* Status indicator */}
      <div className={`mt-4 p-2 rounded text-xs text-center font-mono ${
        utilization > 80
          ? 'bg-red-500/10 text-red-300 border border-red-500/20'
          : utilization > 50
          ? 'bg-yellow-500/10 text-yellow-300 border border-yellow-500/20'
          : 'bg-green-500/10 text-green-300 border border-green-500/20'
      }`}>
        {utilization > 80 ? '⚠️ High Load' : utilization > 50 ? '⚡ Moderate Load' : '✓ Normal'}
      </div>
    </div>
  )
}
