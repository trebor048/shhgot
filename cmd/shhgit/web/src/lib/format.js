// Shared formatting helpers. Kept dependency-free so they are trivial to unit
// test and cannot pull a component tree into a utility import.

export const PRIORITY = {
  3: { key: 'crit', label: 'Critical', dot: 'bg-red-500', text: 'text-red-400', border: 'border-l-red-500' },
  2: { key: 'high', label: 'High', dot: 'bg-orange-500', text: 'text-orange-400', border: 'border-l-orange-500' },
  1: { key: 'med', label: 'Medium', dot: 'bg-yellow-500', text: 'text-yellow-300', border: 'border-l-yellow-500' },
  0: { key: 'low', label: 'Low', dot: 'bg-green-500', text: 'text-green-400', border: 'border-l-green-500' },
}

export function priorityMeta(p) {
  return PRIORITY[p] || PRIORITY[0]
}

// Relative "3m ago" style timestamp; falls back to a locale time for recent
// events so a freshly detected match reads as "just now".
export function relTime(iso) {
  if (!iso) return '-'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '-'
  const secs = Math.round((Date.now() - t) / 1000)
  if (secs < 5) return 'just now'
  if (secs < 60) return `${secs}s ago`
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}

export function localeTime(iso) {
  if (!iso) return '-'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleTimeString()
}

export function shortRepo(u) {
  if (!u) return ''
  return String(u)
    .replace(/\.git$/, '')
    .replace(/^https?:\/\//, '')
    .replace(/^www\./, '')
}

// owner/repo form for compact display.
export function repoName(u) {
  const parts = shortRepo(u).split('/')
  return parts.length >= 3 ? parts.slice(-2).join('/') : shortRepo(u)
}

export function repoFromUrl(u) {
  const parts = shortRepo(u).split('/')
  return parts.length >= 3 ? parts.slice(-2).join('/') : shortRepo(u)
}

export function short(s, n = 60) {
  s = String(s == null ? '' : s)
  return s.length > n ? s.slice(0, n - 1) + '\u2026' : s
}

// Mask a captured secret for display: keep a short prefix/suffix so an operator
// can identify it without the full value sitting on screen or in a screenshot.
export function maskSecret(v) {
  const s = String(v == null ? '' : v)
  if (s.length <= 10) return '*'.repeat(s.length)
  return s.slice(0, 2) + '*'.repeat(Math.min(24, s.length - 6)) + s.slice(-4)
}

export function truncateMiddle(s, head = 8, tail = 6) {
  s = String(s == null ? '' : s)
  if (s.length <= head + tail + 3) return s
  return `${s.slice(0, head)}\u2026${s.slice(-tail)}`
}

// Only http(s) URLs are rendered as links. Anything else is shown as text so a
// captured `javascript:` or `data:` value cannot become a clickable payload.
export function safeUrl(u) {
  const s = String(u == null ? '' : u).trim()
  return /^https?:\/\//i.test(s) ? s : ''
}

// ---------------------------------------------------------------------------
// ANSI -> styled spans
//
// The scanner's log ring buffer carries raw SGR colour sequences. The old
// dashboard translated them client-side; this is the same approach, but output
// is a list of {text, className} runs rather than an HTML string, so React owns
// the escaping and a log line can never inject markup.
// ---------------------------------------------------------------------------

// sgrToClass returns the classes a single SGR parameter list selects, or null
// when nothing in it is recognised (meaning "leave the current style alone").
// Returning null is distinct from returning [] - the latter is an explicit
// reset (ESC[0m) and must clear the active colour.
function sgrToClass(params) {
  // Minimal SGR support: reset, bold/dim, and the 8 basic + bright colours.
  // That covers what fatih/color emits for the scanner's log levels.
  const classes = []
  for (const p of params) {
    switch (p) {
      case 0: return []
      case 1: classes.push('font-bold'); break
      case 2: classes.push('opacity-75'); break
      case 22: break
      case 30: classes.push('text-slate-500'); break
      case 31: classes.push('text-red-400'); break
      case 32: classes.push('text-green-400'); break
      case 33: classes.push('text-yellow-300'); break
      case 34: classes.push('text-blue-400'); break
      case 35: classes.push('text-fuchsia-400'); break
      case 36: classes.push('text-cyan-400'); break
      case 37: classes.push('text-slate-200'); break
      case 39: classes.push('text-slate-300'); break
      case 90: classes.push('text-slate-600'); break
      case 91: classes.push('text-red-300'); break
      case 92: classes.push('text-green-300'); break
      case 93: classes.push('text-yellow-200'); break
      case 94: classes.push('text-blue-300'); break
      case 95: classes.push('text-fuchsia-300'); break
      case 96: classes.push('text-cyan-300'); break
      case 97: classes.push('text-white'); break
      default: break
    }
  }
  return classes.length ? classes : null
}

const ANSI_RE = /\u001B\[([0-9;]*)m/g

// ansiToRuns returns [{text, className}] runs for one log line. Unknown escape
// sequences are stripped rather than printed, so raw control bytes never reach
// the DOM.
export function ansiToRuns(line) {
  const s = String(line == null ? '' : line)
  const runs = []
  let last = 0
  let classes = []
  let m
  ANSI_RE.lastIndex = 0
  while ((m = ANSI_RE.exec(s)) !== null) {
    if (m.index > last) {
      runs.push({ text: s.slice(last, m.index), className: classes.join(' ') })
    }
    const params = m[1] === '' ? [0] : m[1].split(';').map((x) => parseInt(x, 10) || 0)
    // null means "unrecognised, keep current style"; [] is an explicit reset.
    const next = sgrToClass(params)
    if (next !== null) classes = next
    last = m.index + m[0].length
  }
  if (last < s.length) {
    runs.push({ text: s.slice(last), className: classes.join(' ') })
  }
  if (runs.length === 0) runs.push({ text: '', className: '' })
  return runs
}

export function fmtDuration(secs) {
  const s = Math.max(0, Math.round(Number(secs) || 0))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const r = s % 60
  if (m < 60) return `${m}m ${r}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function csvEscape(v) {
  return `"${String(v == null ? '' : v).replace(/"/g, '""')}"`
}
