import React from 'react'
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, PieChart, Pie, Cell } from 'recharts'

const COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e']

export default function StatsPanel({ stats }) {
  const sourceData = Object.entries(stats.matches_by_source || {}).map(([name, value]) => ({
    name,
    value,
  }))

  const priorityData = [
    { name: '🔴 Critical (3)', value: stats.matches_by_priority?.[3] || 0 },
    { name: '🟠 High (2)', value: stats.matches_by_priority?.[2] || 0 },
    { name: '🟡 Medium (1)', value: stats.matches_by_priority?.[1] || 0 },
    { name: '🟢 Low (0)', value: stats.matches_by_priority?.[0] || 0 },
  ].filter(d => d.value > 0)

  const topSigsData = (stats.top_signatures || []).slice(0, 10).map(sig => ({
    name: sig.name.substring(0, 25),
    value: sig.count,
  }))

  return (
    <div className="p-6 space-y-6">
      {/* Key metrics */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        {[
          {
            label: 'Total Matches',
            value: stats.total_matches?.toLocaleString() || 0,
            color: 'from-cyan-600 to-blue-600',
          },
          {
            label: 'Critical Matches',
            value: (stats.matches_by_priority?.[3] || 0).toLocaleString(),
            color: 'from-red-600 to-orange-600',
          },
          {
            label: 'Unique Signatures',
            value: Object.keys(stats.matches_by_signature || {}).length,
            color: 'from-purple-600 to-pink-600',
          },
          {
            label: 'Data Sources',
            value: Object.keys(stats.matches_by_source || {}).length,
            color: 'from-green-600 to-emerald-600',
          },
        ].map((metric, i) => (
          <div key={i} className={`bg-gradient-to-br ${metric.color} p-6 rounded-lg`}>
            <p className="text-white/80 text-sm font-medium mb-2">{metric.label}</p>
            <p className="text-3xl font-bold text-white">{metric.value}</p>
          </div>
        ))}
      </div>

      {/* Charts */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Source distribution */}
        {sourceData.length > 0 && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">Matches by Source</h3>
            <ResponsiveContainer width="100%" height={300}>
              <BarChart data={sourceData}>
                <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
                <XAxis dataKey="name" stroke="#94a3b8" />
                <YAxis stroke="#94a3b8" />
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

        {/* Priority distribution */}
        {priorityData.length > 0 && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">Matches by Priority</h3>
            <ResponsiveContainer width="100%" height={300}>
              <PieChart>
                <Pie
                  data={priorityData}
                  cx="50%"
                  cy="50%"
                  labelLine={false}
                  label={({ name, value }) => `${name}: ${value}`}
                  outerRadius={100}
                  fill="#8884d8"
                  dataKey="value"
                >
                  {priorityData.map((entry, index) => (
                    <Cell key={`cell-${index}`} fill={COLORS[index % COLORS.length]} />
                  ))}
                </Pie>
                <Tooltip
                  contentStyle={{
                    backgroundColor: '#1e293b',
                    border: '1px solid #475569',
                    borderRadius: '0.5rem',
                  }}
                  labelStyle={{ color: '#cbd5e1' }}
                />
              </PieChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>

      {/* Top signatures */}
      {topSigsData.length > 0 && (
        <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
          <h3 className="font-semibold mb-4 text-slate-100">Top 10 Signatures</h3>
          <ResponsiveContainer width="100%" height={300}>
            <BarChart
              data={topSigsData}
              layout="vertical"
              margin={{ top: 5, right: 30, left: 150, bottom: 5 }}
            >
              <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
              <XAxis type="number" stroke="#94a3b8" />
              <YAxis dataKey="name" type="category" stroke="#94a3b8" width={150} />
              <Tooltip
                contentStyle={{
                  backgroundColor: '#1e293b',
                  border: '1px solid #475569',
                  borderRadius: '0.5rem',
                }}
                labelStyle={{ color: '#cbd5e1' }}
              />
              <Bar dataKey="value" fill="#f59e0b" radius={[0, 8, 8, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Detailed breakdown */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {/* By Signature */}
        {Object.keys(stats.matches_by_signature || {}).length > 0 && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">All Signatures</h3>
            <div className="space-y-2 max-h-96 overflow-y-auto">
              {Object.entries(stats.matches_by_signature || {})
                .sort(([, a], [, b]) => b - a)
                .map(([sig, count]) => (
                  <div key={sig} className="flex items-center justify-between p-3 bg-slate-900/50 rounded">
                    <span className="text-sm truncate text-slate-300">{sig}</span>
                    <span className="text-sm font-semibold text-cyan-400 flex-shrink-0 ml-2">
                      {count.toLocaleString()}
                    </span>
                  </div>
                ))}
            </div>
          </div>
        )}

        {/* By Source */}
        {Object.keys(stats.matches_by_source || {}).length > 0 && (
          <div className="bg-slate-800/50 border border-slate-700 rounded-lg p-6">
            <h3 className="font-semibold mb-4 text-slate-100">By Source</h3>
            <div className="space-y-2">
              {Object.entries(stats.matches_by_source || {})
                .sort(([, a], [, b]) => b - a)
                .map(([source, count]) => (
                  <div key={source} className="flex items-center justify-between p-3 bg-slate-900/50 rounded">
                    <span className="text-sm text-slate-300">{source}</span>
                    <span className="text-sm font-semibold text-cyan-400">
                      {count.toLocaleString()}
                    </span>
                  </div>
                ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
