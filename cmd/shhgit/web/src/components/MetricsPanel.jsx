import React, { useEffect, useState } from 'react'
import { api } from '../lib/api'

// ms renders a Go time.Duration value (nanoseconds in JSON) compactly.
function ms(ns) {
  const v = Number(ns) || 0
  if (v <= 0) return '0ms'
  const n = v / 1e6
  if (n < 1) return '<1ms'
  if (n < 1000) return `${n.toFixed(n < 10 ? 1 : 0)}ms`
  return `${(n / 1000).toFixed(2)}s`
}

// MetricsPanel shows the in-process collector's per-subsystem latency and
// counters, polled from /api/metrics. It is disabled (and empty) when the
// operator sets metrics.enabled: false.
export default function MetricsPanel() {
  const [snapshot, setSnapshot] = useState(null)
  const [error, setError] = useState(null)

  useEffect(() => {
    let cancelled = false
    const load = () => {
      api
        .metrics()
        .then((data) => {
          if (cancelled) return
          setSnapshot(data || {})
          setError(null)
        })
        .catch((e) => {
          if (!cancelled) setError(e.message)
        })
    }
    load()
    const t = setInterval(load, 5000)
    return () => {
      cancelled = true
      clearInterval(t)
    }
  }, [])

  const rows = snapshot
    ? Object.entries(snapshot).sort(([a], [b]) => a.localeCompare(b))
    : []

  return (
    <div className="bg-slate-900/60 border border-slate-800 rounded-lg p-4">
      <h3 className="font-semibold text-sm text-slate-200 mb-3">Subsystem metrics</h3>
      {error ? (
        <div className="text-xs text-red-400">{error}</div>
      ) : rows.length === 0 ? (
        <div className="text-xs text-slate-500 italic">No metrics recorded yet.</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-slate-500 text-left">
                <th className="py-1 pr-3">Subsystem</th>
                <th className="py-1 pr-3">Ops</th>
                <th className="py-1 pr-3">Errors</th>
                <th className="py-1 pr-3">p50</th>
                <th className="py-1 pr-3">p95</th>
                <th className="py-1">p99</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(([name, s]) => (
                <tr key={name} className="border-t border-slate-800">
                  <td className="py-1 pr-3 text-cyan-400">{name}</td>
                  <td className="py-1 pr-3">{(s.Ops || 0).toLocaleString()}</td>
                  <td className={`py-1 pr-3 ${s.Errors ? 'text-red-400' : ''}`}>
                    {(s.Errors || 0).toLocaleString()}
                  </td>
                  <td className="py-1 pr-3">{ms(s.P50)}</td>
                  <td className="py-1 pr-3">{ms(s.P95)}</td>
                  <td className="py-1">{ms(s.P99)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
