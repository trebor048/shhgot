import React, { useMemo, useState, useEffect } from 'react'
import { LineChart, Line, AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, BarChart, Bar } from 'recharts'
import { Activity, Zap, TrendingUp } from 'lucide-react'

export default function Dashboard({ matches, stats }) {
  const [scanProgress, setScanProgress] = useState(0)
  const [regexStats, setRegexStats] = useState(null)

  // Fetch regex optimizer stats
  useEffect(() => {
    fetch('/api/stats/regex')
      .then(r => r.json())
      .then(data => setRegexStats(data))
      .catch(() => {})
  }, [])

  // Timeline data - memoized and optimized
  const timelineData = useMemo(() => {
    const hourBuckets = {}
    matches.forEach(match => {
      const date = new Date(match.timestamp)
      const hour = new Date(date.getFullYear(), date.getMonth(), date.getDate(), date.getHours())
      const key = hour.toISOString()
      hourBuckets[key] = (hourBuckets[key] || 0) + 1
    })
    return Object.entries(hourBuckets)
      .sort(([a], [b]) => a.localeCompare(b))
      .slice(-24)
      .map(([time, count]) => ({
        time: new Date(time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
        count,
      }))
  }, [matches])

  // Priority distribution - optimized
  const priorityDistribution = useMemo(() => {
    return [
      { name: 'Critical', value: stats.matches_by_priority?.[3] || 0, color: '#ef4444' },
      { name: 'High', value: stats.matches_by_priority?.[2] || 0, color: '#f97316' },
      { name: 'Medium', value: stats.matches_by_priority?.[1] || 0, color: '#eab308' },
      { name: 'Low', value: stats.matches_by_priority?.[0] || 0, color: '#22c55e' },
    ]
  }, [stats])

  const priorityTrend = useMemo(() => {
    const buckets = { 3: {}, 2: {}, 1: {}, 0: {} }
    matches.forEach(match => {
      const date = new Date(match.timestamp)
      const hour = new Date(date.getFullYear(), date.getMonth(), date.getDate(), date.getHours())
      const key = hour.toISOString()
      buckets[match.priority][key] = (buckets[match.priority][key] || 0) + 1
    })

    const allHours = [...new Set(matches.map(m => {
      const date = new Date(m.timestamp)
      return new Date(date.getFullYear(), date.getMonth(), date.getDate(), date.getHours()).toISOString()
    }))].sort()

    return allHours.slice(-24).map(hour => ({
      time: new Date(hour).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
      critical: buckets[3][hour] || 0,
      high: buckets[2][hour] || 0,
      medium: buckets[1][hour] || 0,
      low: buckets[0][hour] || 0,
    }))
  }, [matches])

  // Source distribution
  const sourceData = useMemo(() => {
    return Object.entries(stats.matches_by_source || {}).map(([source, count]) => ({
      name: source,
      count,
    })).slice(0, 10)
  }, [stats])

  const averageSeverity = useMemo(() => {
    if (stats.total_matches === 0) return 0
    const total = 
      (stats.matches_by_priority?.[3] || 0) * 3 +
      (stats.matches_by_priority?.[2] || 0) * 2 +
      (stats.matches_by_priority?.[1] || 0) * 1 +
      (stats.matches_by_priority?.[0] || 0) * 0
    return (total / stats.total_matches).toFixed(2)
  }, [stats])

  return (
    <div className="p-6 space-y-6">
      {/* Top stats cards */}
      <div className="grid grid-cols-1 md:grid-cols-5 gap-4">
        {[
          { label: 'Total Matches', value: stats.total_matches, color: 'from-blue-600 to-cyan-600', icon: Activity },
          { label: 'Critical', value: stats.matches_by_priority?.[3] || 0, color: 'from-red-600 to-red-700', icon: TrendingUp },
          { label: 'High', value: stats.matches_by_priority?.[2] || 0, color: 'from-orange-600 to-orange-700' },
          { label: 'Medium', value: stats.matches_by_priority?.[1] || 0, color: 'from-yellow-600 to-yellow-700' },
          { label: 'Low', value: stats.matches_by_priority?.[0] || 0, color: 'from-green-600 to-green-700' },
        ].map((stat, i) => (
          <div key={i} className={`bg-gradient-to-br ${stat.color} p-4 rounded-lg hover:shadow-lg transition-shadow`}>
            <p className="text-white/80 text-xs font-medium uppercase tracking-wide mb-1 flex items-center gap-2">
              {stat.icon && <stat.icon size={14} />}
              {stat.label}
            </p>
            <p className="text-2xl font-bold text-white">{stat.value.toLocaleString()}</p>
          </div>
        ))}
      </div>

      {/* Scanning Progress (if available) */}
      {regexStats && (
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <div className="flex items-center justify-between mb-4">
            <h3 className="font-semibold text-slate-100 flex items-center gap-2">
              <Zap size={18} className="text-yellow-400" />
              Regex Engine Performance
            </h3>
            <span className="text-xs text-slate-400">Optimized</span>
          </div>
          
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div className="bg-slate-900/50 p-3 rounded">
              <p className="text-xs text-slate-400 mb-1">Cache Hit Rate</p>
              <p className="text-lg font-bold text-cyan-400">{(regexStats.cache_hit_rate || 0).toFixed(1)}%</p>
            </div>
            <div className="bg-slate-900/50 p-3 rounded">
              <p className="text-xs text-slate-400 mb-1">Patterns Compiled</p>
              <p className="text-lg font-bold text-green-400">{regexStats.patterns_compiled}</p>
            </div>
            <div className="bg-slate-900/50 p-3 rounded">
              <p className="text-xs text-slate-400 mb-1">Cache Usage</p>
              <p className="text-lg font-bold text-orange-400">{regexStats.cache_size}/{regexStats.cache_max}</p>
            </div>
            <div className="bg-slate-900/50 p-3 rounded">
              <p className="text-xs text-slate-400 mb-1">Workers</p>
              <p className="text-lg font-bold text-purple-400">{regexStats.workers_available}/{regexStats.max_workers}</p>
            </div>
          </div>
        </div>
      )}

      {/* Timeline Chart */}
      {timelineData.length > 0 && (
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Match Timeline (Last 24 Hours)</h3>
          <ResponsiveContainer width="100%" height={300}>
            <AreaChart data={timelineData} margin={{ top: 10, right: 30, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id="colorCount" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#0891b2" stopOpacity={0.8} />
                  <stop offset="95%" stopColor="#0891b2" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
              <XAxis dataKey="time" stroke="#94a3b8" style={{ fontSize: '12px' }} />
              <YAxis stroke="#94a3b8" style={{ fontSize: '12px' }} />
              <Tooltip
                contentStyle={{
                  backgroundColor: '#1e293b',
                  border: '1px solid #475569',
                  borderRadius: '0.5rem',
                }}
                labelStyle={{ color: '#cbd5e1' }}
              />
              <Area type="monotone" dataKey="count" stroke="#0891b2" fillOpacity={1} fill="url(#colorCount)" />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Priority Distribution & Trend */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {/* Priority Distribution Bar */}
        {priorityDistribution.some(p => p.value > 0) && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">Priority Distribution</h3>
            <ResponsiveContainer width="100%" height={250}>
              <BarChart data={priorityDistribution}>
                <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
                <XAxis dataKey="name" stroke="#94a3b8" style={{ fontSize: '12px' }} />
                <YAxis stroke="#94a3b8" style={{ fontSize: '12px' }} />
                <Tooltip
                  contentStyle={{
                    backgroundColor: '#1e293b',
                    border: '1px solid #475569',
                    borderRadius: '0.5rem',
                  }}
                  labelStyle={{ color: '#cbd5e1' }}
                />
                <Bar dataKey="value" fill="#0891b2" radius={[8, 8, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}

        {/* Priority Trend */}
        {priorityTrend.length > 0 && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">Priority Trend</h3>
            <ResponsiveContainer width="100%" height={250}>
              <LineChart data={priorityTrend}>
                <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
                <XAxis dataKey="time" stroke="#94a3b8" style={{ fontSize: '12px' }} />
                <YAxis stroke="#94a3b8" style={{ fontSize: '12px' }} />
                <Tooltip
                  contentStyle={{
                    backgroundColor: '#1e293b',
                    border: '1px solid #475569',
                    borderRadius: '0.5rem',
                  }}
                  labelStyle={{ color: '#cbd5e1' }}
                />
                <Line type="monotone" dataKey="critical" stroke="#ef4444" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="high" stroke="#f97316" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="medium" stroke="#eab308" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="low" stroke="#22c55e" strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      {/* Bottom stats grid */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Most Common Signature</h3>
          {stats.top_signatures?.[0] ? (
            <div>
              <p className="text-lg font-bold text-cyan-400 truncate">{stats.top_signatures[0].name}</p>
              <p className="text-sm text-slate-400 mt-2">
                <span className="text-cyan-300 font-semibold">{stats.top_signatures[0].count.toLocaleString()}</span> detections
              </p>
            </div>
          ) : (
            <p className="text-slate-400">No data yet</p>
          )}
        </div>

        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Average Severity</h3>
          {stats.total_matches > 0 ? (
            <div>
              <p className="text-lg font-bold text-yellow-400">{averageSeverity}</p>
              <p className="text-sm text-slate-400 mt-2">Out of 3.0</p>
              <div className="mt-3 w-full bg-slate-700 rounded-full h-2">
                <div 
                  className="bg-yellow-400 h-2 rounded-full" 
                  style={{ width: `${(parseFloat(averageSeverity) / 3) * 100}%` }}
                />
              </div>
            </div>
          ) : (
            <p className="text-slate-400">No data yet</p>
          )}
        </div>

        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Last Update</h3>
          <p className="text-lg font-bold text-green-400">
            {stats.last_updated
              ? new Date(stats.last_updated).toLocaleTimeString()
              : 'Never'}
          </p>
          <p className="text-sm text-slate-400 mt-2">
            {stats.last_updated
              ? `${Math.round((Date.now() - new Date(stats.last_updated).getTime()) / 1000)}s ago`
              : '-'}
          </p>
        </div>
      </div>
    </div>
  )
}
