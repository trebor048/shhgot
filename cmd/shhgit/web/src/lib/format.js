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

// Verified severity is the scanner's assessment of a captured secret (after
// validation), which is a different axis from the rule's configured priority.
// Keys mirror the `severity` strings core.Classify emits: low | medium | high |
// critical (never "info"). Ordered most- to least-severe so Object.keys is
// sortable.
export const SEVERITY = {
  critical: {
    key: 'critical',
    label: 'Critical',
    dot: 'bg-red-500',
    text: 'text-red-400',
    badge: 'text-red-300 bg-red-500/15 border-red-500/40',
  },
  high: {
    key: 'high',
    label: 'High',
    dot: 'bg-orange-500',
    text: 'text-orange-400',
    badge: 'text-orange-300 bg-orange-500/15 border-orange-500/40',
  },
  medium: {
    key: 'medium',
    label: 'Medium',
    dot: 'bg-yellow-500',
    text: 'text-yellow-300',
    badge: 'text-yellow-200 bg-yellow-500/15 border-yellow-500/40',
  },
  low: {
    key: 'low',
    label: 'Low',
    dot: 'bg-green-500',
    text: 'text-green-400',
    badge: 'text-green-300 bg-green-500/15 border-green-500/40',
  },
}

// severityMeta normalises the API's severity string (case-insensitively) to its
// display metadata, or null for an absent/unknown value so a missing severity
// never renders as a misleading badge.
export function severityMeta(s) {
  if (s == null || s === '') return null
  return SEVERITY[String(s).trim().toLowerCase()] || null
}

// Relative "3m ago" style timestamp; recent events read as "just now".
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

export function shortRepo(u) {
  if (!u) return ''
  return String(u)
    .replace(/\.git$/, '')
    .replace(/^https?:\/\//, '')
    .replace(/^www\./, '')
}

export function short(s, n = 60) {
  s = String(s == null ? '' : s)
  return s.length > n ? s.slice(0, n - 1) + '\u2026' : s
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

// CSI_FINAL_RE matches a CSI sequence's final byte (the letter that terminates
// ESC [ params <final>). Used to walk past SGR and every other CSI escape.
const CSI_FINAL_RE = /[A-Za-z@-~]/

// ansiToRuns returns [{text, className, href}] runs for one log line.
//
// The scanner emits three kinds of control sequence into the log ring buffer:
//   - SGR colour codes (ESC [ ... m), rendered as CSS classes;
//   - OSC 8 hyperlinks (ESC ] 8 ; params ; URL ST), which wrap a visible label
//     and are surfaced as a real link so the Logs tab matches the terminal;
//   - every other escape or control byte, stripped rather than printed so raw
//     controls never reach the DOM.
export function ansiToRuns(line) {
  const s = String(line == null ? '' : line)
  const runs = []
  let text = ''
  let classes = []
  let href = ''
  let i = 0

  const flush = () => {
    if (text !== '') {
      runs.push({ text, className: classes.join(' '), href })
      text = ''
    }
  }

  while (i < s.length) {
    const ch = s[i]

    if (ch === '\u001B') {
      // Close the current run before the sequence changes colour or link state.
      flush()
      const next = s[i + 1]
      if (next === ']') {
        // OSC sequence: ESC ] ... (BEL | ESC \). Only OSC 8 carries a link URL.
        let j = i + 2
        while (j < s.length && s[j] !== '\u0007' && !(s[j] === '\u001B' && s[j + 1] === '\\')) j++
        const body = s.slice(i + 2, j)
        const link = /^8;([^;]*);(.*)$/.exec(body)
        // An empty URL is the closing half of an OSC 8 pair, so it clears href.
        if (link) href = link[2]
        i = j < s.length ? (s[j] === '\u0007' ? j + 1 : j + 2) : j
        continue
      }
      if (next === '[') {
        // CSI sequence: ESC [ params <final>. Only "m" changes colour.
        let j = i + 2
        while (j < s.length && !CSI_FINAL_RE.test(s[j])) j++
        if (s[j] === 'm') {
          const raw = s.slice(i + 2, j)
          const params = raw === '' ? [0] : raw.split(';').map((x) => parseInt(x, 10) || 0)
          // null means "unrecognised, keep current style"; [] is an explicit reset.
          const chosen = sgrToClass(params)
          if (chosen !== null) classes = chosen
        }
        i = j < s.length ? j + 1 : j
        continue
      }
      // Any other escape: skip the ESC and the byte that follows it.
      i += next ? 2 : 1
      continue
    }

    // Keep tabs and newlines; drop every other C0 control (notably CR, which
    // white-space: pre-wrap would otherwise render as a spurious line break).
    if (ch === '\n' || ch === '\t' || ch >= ' ') {
      text += ch
    }
    i++
  }

  flush()
  if (runs.length === 0) runs.push({ text: '', className: '', href: '' })
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
  let s = String(v == null ? '' : v)
  // Neutralise spreadsheet formula injection: Excel/Sheets evaluate a cell that
  // begins with =, +, -, @ or a leading control char (tab/CR). A leading single
  // quote forces the cell to be treated as text.
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`
  return `"${s.replace(/"/g, '""')}"`
}
