import React from 'react'
import { LineChart, Line, AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'

export default function Dashboard({ matches, stats }) {
  // Timeline data - aggregate matches by hour
  const timelineData = React.useMemo(() => {
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

  // Priority trend
  const priorityTrend = React.useMemo(() => {
    const buckets = { 3: [], 2: [], 1: [], 0: [] }
    matches.forEach(match => {
      const date = new Date(match.timestamp)
      const hour = new Date(date.getFullYear(), date.getMonth(), date.getDate(), date.getHours())
      const key = hour.toISOString()
      if (!buckets[match.priority]) buckets[match.priority] = {}
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

  return (
    <div className="p-6 space-y-6">
      {/* Top stats */}
      <div className="grid grid-cols-1 md:grid-cols-5 gap-4">
        {[
          { label: 'Total Matches', value: stats.total_matches, color: 'from-blue-600 to-cyan-600' },
          { label: 'Critical', value: stats.matches_by_priority?.[3] || 0, color: 'from-red-600 to-red-700' },
          { label: 'High', value: stats.matches_by_priority?.[2] || 0, color: 'from-orange-600 to-orange-700' },
          { label: 'Medium', value: stats.matches_by_priority?.[1] || 0, color: 'from-yellow-600 to-yellow-700' },
          { label: 'Low', value: stats.matches_by_priority?.[0] || 0, color: 'from-green-600 to-green-700' },
        ].map((stat, i) => (
          <div key={i} className={`bg-gradient-to-br ${stat.color} p-4 rounded-lg`}>
            <p className="text-white/80 text-xs font-medium uppercase tracking-wide mb-1">{stat.label}</p>
            <p className="text-2xl font-bold text-white">{stat.value.toLocaleString()}</p>
          </div>
        ))}
      </div>

      {/* Timeline */}
      {timelineData.length > 0 && (
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Match Timeline (Last 24 Hours)</h3>
          <ResponsiveContainer width="100%" height={300}>
            <AreaChart data={timelineData}>
              <defs>
                <linearGradient id="colorCount" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#0891b2" stopOpacity={0.8} />
                  <stop offset="95%" stopColor="#0891b2" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
              <XAxis dataKey="time" stroke="#94a3b8" />
              <YAxis stroke="#94a3b8" />
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

      {/* Priority trend */}
      {priorityTrend.length > 0 && (
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Priority Distribution Over Time</h3>
          <ResponsiveContainer width="100%" height={300}>
            <LineChart data={priorityTrend}>
              <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
              <XAxis dataKey="time" stroke="#94a3b8" />
              <YAxis stroke="#94a3b8" />
              <Tooltip
                contentStyle={{
                  backgroundColor: '#1e293b',
                  border: '1px solid #475569',
                  borderRadius: '0.5rem',
                }}
                labelStyle={{ color: '#cbd5e1' }}
              />
              <Line type="monotone" dataKey="critical" stroke="#ef4444" strokeWidth={2} />
              <Line type="monotone" dataKey="high" stroke="#f97316" strokeWidth={2} />
              <Line type="monotone" dataKey="medium" stroke="#eab308" strokeWidth={2} />
              <Line type="monotone" dataKey="low" stroke="#22c55e" strokeWidth={2} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Quick info */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Most Common Signature</h3>
          {stats.top_signatures?.[0] ? (
            <div>
              <p className="text-lg font-bold text-cyan-400">{stats.top_signatures[0].name}</p>
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
              <p className="text-lg font-bold text-yellow-400">
                {(
                  (
                    (stats.matches_by_priority?.[3] || 0) * 3 +
                    (stats.matches_by_priority?.[2] || 0) * 2 +
                    (stats.matches_by_priority?.[1] || 0) * 1 +
                    (stats.matches_by_priority?.[0] || 0) * 0
                  ) / stats.total_matches
                ).toFixed(2)}
              </p>
              <p className="text-sm text-slate-400 mt-2">Out of 3.0</p>
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
