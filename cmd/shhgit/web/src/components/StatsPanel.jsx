import React from 'react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

const PRIORITY_COLORS = { 3: '#ef4444', 2: '#f97316', 1: '#eab308', 0: '#22c55e' }

const tooltipStyle = {
  backgroundColor: '#0f172a',
  border: '1px solid #334155',
  borderRadius: '0.5rem',
  color: '#e2e8f0',
}

export default function StatsPanel({ stats }) {
  const sourceData = Object.entries(stats.matches_by_source || {})
    .map(([name, value]) => ({ name, value }))
    .sort((a, b) => b.value - a.value)

  const priorityData = [3, 2, 1, 0]
    .map((p) => ({
      p,
      name: ['', 'Medium', 'High', 'Critical'][p] || 'Low',
      value: stats.matches_by_priority?.[p] || 0,
    }))
    .filter((d) => d.value > 0)

  const topSigs = (stats.top_signatures || []).map((s) => ({
    name: s.name.length > 22 ? s.name.slice(0, 21) + '\u2026' : s.name,
    value: s.count,
  }))

  const total = stats.total_matches || 0
  const severity =
    total > 0
      ? (
          ((stats.matches_by_priority?.[3] || 0) * 3 +
            (stats.matches_by_priority?.[2] || 0) * 2 +
            (stats.matches_by_priority?.[1] || 0) * 1) /
          total
        ).toFixed(2)
      : '0.00'

  const cards = [
    { label: 'Total matches', value: total, cls: 'from-cyan-600 to-blue-600' },
    { label: 'Critical', value: stats.matches_by_priority?.[3] || 0, cls: 'from-red-600 to-red-700' },
    { label: 'Unique signatures', value: Object.keys(stats.matches_by_signature || {}).length, cls: 'from-fuchsia-600 to-purple-600' },
    { label: 'Avg severity', value: `${severity}/3`, cls: 'from-amber-600 to-orange-600' },
  ]

  const noData = total === 0

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        {cards.map((c) => (
          <div key={c.label} className={`bg-gradient-to-br ${c.cls} rounded-lg p-4`}>
            <div className="text-white/80 text-[11px] uppercase tracking-wide">{c.label}</div>
            <div className="text-2xl font-bold text-white mt-1">{typeof c.value === 'number' ? c.value.toLocaleString() : c.value}</div>
          </div>
        ))}
      </div>

      {noData && <div className="text-center text-slate-500 italic py-16">No matches yet.</div>}

      {!noData && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
          {sourceData.length > 0 && (
            <Card title="Matches by source">
              <ResponsiveContainer width="100%" height={280}>
                <BarChart data={sourceData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#1e293b" />
                  <XAxis dataKey="name" stroke="#94a3b8" fontSize={11} />
                  <YAxis stroke="#94a3b8" fontSize={11} allowDecimals={false} />
                  <Tooltip contentStyle={tooltipStyle} cursor={{ fill: '#1e293b55' }} />
                  <Bar dataKey="value" fill="#0891b2" radius={[6, 6, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </Card>
          )}

          {priorityData.length > 0 && (
            <Card title="Matches by priority">
              <ResponsiveContainer width="100%" height={280}>
                <PieChart>
                  <Pie
                    data={priorityData}
                    dataKey="value"
                    nameKey="name"
                    cx="50%"
                    cy="50%"
                    outerRadius={95}
                    label={({ name, value }) => `${name}: ${value}`}
                  >
                    {priorityData.map((d) => (
                      <Cell key={d.p} fill={PRIORITY_COLORS[d.p]} />
                    ))}
                  </Pie>
                  <Tooltip contentStyle={tooltipStyle} />
                  <Legend wrapperStyle={{ fontSize: 12 }} />
                </PieChart>
              </ResponsiveContainer>
            </Card>
          )}

          {topSigs.length > 0 && (
            <Card title="Top signatures" className="lg:col-span-2">
              <ResponsiveContainer width="100%" height={Math.max(200, topSigs.length * 34)}>
                <BarChart data={topSigs} layout="vertical" margin={{ left: 160, right: 24 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="#1e293b" />
                  <XAxis type="number" stroke="#94a3b8" fontSize={11} allowDecimals={false} />
                  <YAxis dataKey="name" type="category" stroke="#94a3b8" width={155} fontSize={11} />
                  <Tooltip contentStyle={tooltipStyle} cursor={{ fill: '#1e293b55' }} />
                  <Bar dataKey="value" fill="#f59e0b" radius={[0, 6, 6, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </Card>
          )}
        </div>
      )}
    </div>
  )
}

function Card({ title, children, className = '' }) {
  return (
    <div className={`bg-slate-900/60 border border-slate-800 rounded-lg p-4 ${className}`}>
      <h3 className="font-semibold text-sm text-slate-200 mb-3">{title}</h3>
      {children}
    </div>
  )
}
