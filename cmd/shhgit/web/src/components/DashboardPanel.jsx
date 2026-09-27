import React, { useMemo } from 'react'
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { relTime } from '../lib/format'

const tooltipStyle = {
  backgroundColor: '#0f172a',
  border: '1px solid #334155',
  borderRadius: '0.5rem',
  color: '#e2e8f0',
}

function hourKey(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return null
  return new Date(d.getFullYear(), d.getMonth(), d.getDate(), d.getHours()).toISOString()
}

function label(iso) {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export default function DashboardPanel({ matches, stats }) {
  const timeline = useMemo(() => {
    const buckets = new Map()
    for (const m of matches) {
      const k = hourKey(m.timestamp)
      if (!k) continue
      buckets.set(k, (buckets.get(k) || 0) + 1)
    }
    return [...buckets.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .slice(-24)
      .map(([t, count]) => ({ time: label(t), count }))
  }, [matches])

  const trend = useMemo(() => {
    const buckets = new Map()
    for (const m of matches) {
      const k = hourKey(m.timestamp)
      if (!k) continue
      if (!buckets.has(k)) buckets.set(k, { critical: 0, high: 0, medium: 0, low: 0 })
      const b = buckets.get(k)
      if (m.priority === 3) b.critical++
      else if (m.priority === 2) b.high++
      else if (m.priority === 1) b.medium++
      else b.low++
    }
    return [...buckets.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .slice(-24)
      .map(([t, v]) => ({ time: label(t), ...v }))
  }, [matches])

  const cards = [
    { label: 'Total', value: stats.total_matches || 0, cls: 'from-blue-600 to-cyan-600' },
    { label: 'Critical', value: stats.matches_by_priority?.[3] || 0, cls: 'from-red-600 to-red-700' },
    { label: 'High', value: stats.matches_by_priority?.[2] || 0, cls: 'from-orange-600 to-orange-700' },
    { label: 'Medium', value: stats.matches_by_priority?.[1] || 0, cls: 'from-yellow-600 to-yellow-700' },
    { label: 'Low', value: stats.matches_by_priority?.[0] || 0, cls: 'from-green-600 to-green-700' },
  ]

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        {cards.map((c) => (
          <div key={c.label} className={`bg-gradient-to-br ${c.cls} rounded-lg p-4`}>
            <div className="text-white/80 text-[11px] uppercase tracking-wide">{c.label}</div>
            <div className="text-2xl font-bold text-white mt-1">{c.value.toLocaleString()}</div>
          </div>
        ))}
      </div>

      {timeline.length > 0 ? (
        <Card title="Match timeline (last 24 hours)">
          <ResponsiveContainer width="100%" height={280}>
            <AreaChart data={timeline}>
              <defs>
                <linearGradient id="matchArea" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#0891b2" stopOpacity={0.8} />
                  <stop offset="95%" stopColor="#0891b2" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" stroke="#1e293b" />
              <XAxis dataKey="time" stroke="#94a3b8" fontSize={11} />
              <YAxis stroke="#94a3b8" fontSize={11} allowDecimals={false} />
              <Tooltip contentStyle={tooltipStyle} />
              <Area type="monotone" dataKey="count" stroke="#0891b2" fillOpacity={1} fill="url(#matchArea)" />
            </AreaChart>
          </ResponsiveContainer>
        </Card>
      ) : (
        <div className="text-center text-slate-500 italic py-16">No matches yet.</div>
      )}

      {trend.length > 0 && (
        <Card title="Priority distribution over time">
          <ResponsiveContainer width="100%" height={280}>
            <LineChart data={trend}>
              <CartesianGrid strokeDasharray="3 3" stroke="#1e293b" />
              <XAxis dataKey="time" stroke="#94a3b8" fontSize={11} />
              <YAxis stroke="#94a3b8" fontSize={11} allowDecimals={false} />
              <Tooltip contentStyle={tooltipStyle} />
              <Legend wrapperStyle={{ fontSize: 12 }} />
              <Line type="monotone" dataKey="critical" stroke="#ef4444" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="high" stroke="#f97316" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="medium" stroke="#eab308" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="low" stroke="#22c55e" strokeWidth={2} dot={false} />
            </LineChart>
          </ResponsiveContainer>
        </Card>
      )}

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MiniCard title="Most common signature">
          {stats.top_signatures?.[0] ? (
            <>
              <div className="text-cyan-400 font-semibold truncate" title={stats.top_signatures[0].name}>
                {stats.top_signatures[0].name}
              </div>
              <div className="text-xs text-slate-500 mt-1">
                {stats.top_signatures[0].count.toLocaleString()} detections
              </div>
            </>
          ) : (
            <span className="text-slate-500">no data</span>
          )}
        </MiniCard>

        <MiniCard title="Last update">
          <div className="text-green-400 font-semibold">{relTime(stats.last_updated)}</div>
        </MiniCard>

        <MiniCard title="Active repositories">
          <div className="text-cyan-400 font-semibold">
            {(stats.matches_by_source ? Object.keys(stats.matches_by_source).length : 0).toLocaleString()}
          </div>
          <div className="text-xs text-slate-500 mt-1">distinct sources</div>
        </MiniCard>
      </div>
    </div>
  )
}

function Card({ title, children }) {
  return (
    <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-4">
      <h3 className="font-semibold text-sm text-slate-200 mb-3">{title}</h3>
      {children}
    </div>
  )
}

function MiniCard({ title, children }) {
  return (
    <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-4">
      <h3 className="font-semibold text-sm text-slate-200 mb-3">{title}</h3>
      {children}
    </div>
  )
}
