import React, { useEffect, useState } from 'react'
import { KeyRound, RefreshCw, Save, Wifi } from 'lucide-react'
import { api } from '../lib/api'
import { useToast } from './Toast'

export default function SettingsPanel() {
  const toast = useToast()
  const [view, setView] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [form, setForm] = useState({ provider: '', api_key: '', base_url: '', model: '' })

  const load = () => {
    setLoading(true)
    api
      .settings()
      .then((d) => {
        setView(d)
        setError('')
        setForm({
          provider: d.provider || '',
          api_key: '',
          base_url: d.base_url || '',
          model: d.model || '',
        })
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  const changeProvider = (p) => {
    // Mirror the server's merge rule: switching provider resets endpoint/model
    // to that provider's defaults, but keeps any key the operator typed.
    const d = view?.defaults?.[p]
    setForm((f) => ({
      ...f,
      provider: p,
      base_url: d?.base_url || d?.BaseURL || '',
      model: d?.model || d?.Model || '',
    }))
  }

  const payload = () => ({
    provider: form.provider,
    api_key: form.api_key,
    base_url: form.base_url,
    model: form.model,
  })

  const save = () => {
    setSaving(true)
    api
      .saveSettings(payload())
      .then((d) => {
        if (d?.settings) {
          setView(d.settings)
          setForm((f) => ({ ...f, api_key: '' }))
        }
        toast('Settings saved', 'ok')
      })
      .catch((e) => toast(`Save failed: ${e.message}`, 'error'))
      .finally(() => setSaving(false))
  }

  const test = () => {
    setTesting(true)
    api
      .testSettings(payload())
      .then((d) => {
        if (d.ok) toast(`Connection OK: ${d.message || 'provider responded'}`, 'ok')
        else toast(`Connection failed: ${d.error || 'unknown error'}`, 'error')
      })
      .catch((e) => toast(`Test failed: ${e.message}`, 'error'))
      .finally(() => setTesting(false))
  }

  if (loading) return <div className="text-slate-500 italic">Loading settings…</div>
  if (error)
    return (
      <div className="text-sm text-amber-300 bg-amber-500/10 border border-amber-500/30 rounded p-3">
        AI settings are unavailable: {error}
      </div>
    )

  const inputCls =
    'w-full px-3 py-2 bg-slate-950 border border-slate-700 rounded text-sm focus:outline-none focus:border-cyan-500'

  return (
    <div className="max-w-2xl space-y-4">
      <div className="flex items-center gap-2">
        <KeyRound size={16} className="text-cyan-400" />
        <h3 className="font-semibold">AI provider</h3>
      </div>

      <div>
        <label className="block text-xs text-slate-400 mb-1">Provider</label>
        <select value={form.provider} onChange={(e) => changeProvider(e.target.value)} className={inputCls}>
          {(view?.providers || []).map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </div>

      <div>
        <label className="block text-xs text-slate-400 mb-1">
          API key {view?.api_key_set && <span className="text-green-400">(one is stored; leave blank to keep it)</span>}
        </label>
        <input
          type="password"
          value={form.api_key}
          autoComplete="off"
          onChange={(e) => setForm((f) => ({ ...f, api_key: e.target.value }))}
          placeholder={view?.api_key_set ? '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022 stored' : 'not set'}
          className={inputCls}
        />
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label className="block text-xs text-slate-400 mb-1">Base URL (blank = default)</label>
          <input
            value={form.base_url}
            onChange={(e) => setForm((f) => ({ ...f, base_url: e.target.value }))}
            placeholder={view?.defaults?.[form.provider]?.base_url || 'provider default'}
            className={inputCls}
          />
        </div>
        <div>
          <label className="block text-xs text-slate-400 mb-1">Model (blank = default)</label>
          <input
            value={form.model}
            onChange={(e) => setForm((f) => ({ ...f, model: e.target.value }))}
            placeholder={view?.defaults?.[form.provider]?.model || 'provider default'}
            className={inputCls}
          />
        </div>
      </div>

      <div className="flex items-center gap-2 pt-2">
        <button
          onClick={save}
          disabled={saving}
          className="flex items-center gap-1.5 px-3 py-2 rounded bg-cyan-600 hover:bg-cyan-500 disabled:opacity-40 text-white text-sm"
        >
          {saving ? <RefreshCw size={15} className="animate-spin" /> : <Save size={15} />}
          Save
        </button>
        <button
          onClick={test}
          disabled={testing}
          className="flex items-center gap-1.5 px-3 py-2 rounded bg-slate-800 hover:bg-slate-700 disabled:opacity-40 text-slate-200 text-sm"
        >
          {testing ? <RefreshCw size={15} className="animate-spin" /> : <Wifi size={15} />}
          Test connection
        </button>
      </div>

      <p className="text-[11px] text-slate-500 border-t border-slate-800 pt-3">
        The API key is never sent back to the browser; this page only learns whether one is stored. The AI
        Review tab becomes available once a working provider is configured.
      </p>
    </div>
  )
}
