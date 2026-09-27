import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  MessageSquare,
  Plus,
  RefreshCw,
  Send,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react'
import { api, reviewStreamURL, ssePost } from '../lib/api'
import { relTime, short, shortRepo } from '../lib/format'
import { useToast } from './Toast'

const STATUS_STYLE = {
  queued: 'text-slate-300 bg-slate-700/50',
  running: 'text-cyan-300 bg-cyan-500/15 animate-pulse',
  done: 'text-green-300 bg-green-500/15',
  failed: 'text-red-300 bg-red-500/15',
}

// Minimal, safe markdown: only fenced code blocks and paragraphs are treated
// specially; everything else is rendered as text so a model cannot inject
// markup. Good enough for a triage assessment.
function MarkdownLite({ text }) {
  const segments = useMemo(() => {
    const out = []
    const re = /```(\w*)\n?([\s\S]*?)```/g
    let last = 0
    let m
    while ((m = re.exec(text)) !== null) {
      if (m.index > last) out.push({ type: 'text', body: text.slice(last, m.index) })
      out.push({ type: 'code', lang: m[1], body: m[2] })
      last = m.index + m[0].length
    }
    if (last < text.length) out.push({ type: 'text', body: text.slice(last) })
    return out
  }, [text])

  return (
    <div className="space-y-3 text-sm text-slate-300">
      {segments.map((s, i) =>
        s.type === 'code' ? (
          <pre key={i} className="bg-slate-950 border border-slate-800 rounded p-3 overflow-x-auto text-xs">
            <code>{s.body}</code>
          </pre>
        ) : (
          <div key={i} className="whitespace-pre-wrap leading-relaxed">
            {s.body}
          </div>
        ),
      )}
    </div>
  )
}

export default function ReviewPanel({ pendingMatch, onConsumePending, reviewTick }) {
  const toast = useToast()
  const [list, setList] = useState([])
  const [listError, setListError] = useState('')
  const [selectedId, setSelectedId] = useState(null)
  const [detail, setDetail] = useState(null)
  const [assessment, setAssessment] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [chat, setChat] = useState('')
  const [chatBusy, setChatBusy] = useState(false)
  const esRef = useRef(null)
  const chatScrollRef = useRef(null)

  const closeStream = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
      esRef.current = null
    }
    setStreaming(false)
  }, [])

  const loadList = useCallback(() => {
    api
      .reviewList()
      .then((d) => {
        setList(d.reviews || [])
        setListError('')
      })
      .catch((e) => setListError(e.message))
  }, [])

  const loadDetail = useCallback(
    (id) => {
      api
        .reviewGet(id)
        .then((d) => {
          setDetail(d)
          setAssessment(d.assessment || '')
        })
        .catch((e) => toast(`Could not load review: ${e.message}`, 'error'))
    },
    [toast],
  )

  useEffect(() => {
    loadList()
    const id = setInterval(loadList, 4000)
    return () => clearInterval(id)
  }, [loadList])

  useEffect(() => {
    if (reviewTick > 0) loadList()
  }, [reviewTick, loadList])

  // Create a review when a match is handed over from the Matches tab.
  useEffect(() => {
    if (!pendingMatch) return
    const m = pendingMatch
    onConsumePending()
    api
      .reviewCreate({
        match_id: m.id || '',
        signature: m.signature || '',
        secret: (m.matches && m.matches[0]) || m.secret || '',
        file: m.file || '',
        url: m.url || '',
        repo: m.url || '',
        context: m.line ? `scanner: detected at line ${m.line}` : '',
      })
      .then((res) => {
        toast('AI review queued', 'ok')
        setSelectedId(res.id)
        loadList()
      })
      .catch((e) => toast(`Could not start review: ${e.message}`, 'error'))
  }, [pendingMatch, onConsumePending, toast, loadList])

  // Load detail + attach the live stream whenever the selection changes.
  useEffect(() => {
    closeStream()
    if (!selectedId) {
      setDetail(null)
      setAssessment('')
      return
    }
    loadDetail(selectedId)

    const es = new EventSource(reviewStreamURL(selectedId))
    esRef.current = es
    setStreaming(true)
    es.onmessage = (ev) => {
      let data
      try {
        data = JSON.parse(ev.data)
      } catch {
        return
      }
      if (data.type === 'delta') {
        setAssessment((prev) => (data.reset ? data.text || '' : prev + (data.text || '')))
      } else if (data.type === 'done') {
        if (typeof data.assessment === 'string') setAssessment(data.assessment)
        setStreaming(false)
        closeStream()
        loadDetail(selectedId)
        loadList()
      } else if (data.type === 'error') {
        if (typeof data.assessment === 'string') setAssessment(data.assessment)
        toast(`Review error: ${data.error}`, 'error')
        setStreaming(false)
        closeStream()
        loadDetail(selectedId)
        loadList()
      }
    }
    es.onerror = () => {
      // EventSource reconnects; if the review finished the server closes the
      // stream and this fires once. Drop the spinner so the UI is not stuck.
      setStreaming(false)
    }
    return () => closeStream()
  }, [selectedId, closeStream, loadDetail, loadList, toast])

  useEffect(() => {
    if (chatScrollRef.current) chatScrollRef.current.scrollTop = chatScrollRef.current.scrollHeight
  }, [detail, assessment])

  const messages = detail?.messages || []

  const sendChat = async () => {
    const q = chat.trim()
    if (!q || !selectedId || chatBusy) return
    setChatBusy(true)
    setChat('')
    // Optimistically show the question; the server persists it too.
    setDetail((d) => (d ? { ...d, messages: (d.messages || []).concat([{ role: 'user', content: q }]) } : d))
    let reply = ''
    try {
      await ssePost(`/api/review/${encodeURIComponent(selectedId)}/chat`, { message: q }, (ev) => {
        if (ev.type === 'delta') {
          reply += ev.text || ''
          setDetail((d) => {
            if (!d) return d
            const msgs = (d.messages || []).slice()
            const lastMsg = msgs[msgs.length - 1]
            if (lastMsg && lastMsg.role === 'assistant' && lastMsg._live) {
              msgs[msgs.length - 1] = { ...lastMsg, content: reply }
            } else {
              msgs.push({ role: 'assistant', content: reply, _live: true })
            }
            return { ...d, messages: msgs }
          })
        } else if (ev.type === 'error') {
          toast(`Chat error: ${ev.error}`, 'error')
        }
      })
    } catch (e) {
      toast(`Chat failed: ${e.message}`, 'error')
    } finally {
      setChatBusy(false)
      loadDetail(selectedId)
    }
  }

  const remove = (id) => {
    api
      .reviewDelete(id)
      .then(() => {
        if (selectedId === id) setSelectedId(null)
        loadList()
        toast('Review deleted', 'info')
      })
      .catch((e) => toast(`Delete failed: ${e.message}`, 'error'))
  }

  return (
    <div className="flex flex-col lg:flex-row gap-4 h-full min-h-0">
      <div className="lg:w-80 shrink-0 flex flex-col min-h-0">
        <div className="flex items-center gap-2 mb-2">
          <h3 className="text-xs uppercase tracking-wide text-slate-500 flex-1">
            Reviews ({list.length})
          </h3>
          <button onClick={loadList} className="p-1 hover:bg-slate-800 rounded text-slate-400" title="Refresh">
            <RefreshCw size={14} />
          </button>
        </div>

        {listError && (
          <div className="text-xs text-amber-300 bg-amber-500/10 border border-amber-500/30 rounded p-2 mb-2">
            {listError}
          </div>
        )}

        <div className="overflow-y-auto space-y-1.5 flex-1 min-h-0">
          {list.length === 0 && (
            <div className="text-center text-slate-600 italic py-10 text-xs">
              <Sparkles className="mx-auto mb-2 opacity-60" />
              No reviews yet. Open a match and choose <b>AI review</b>.
            </div>
          )}
          {list.map((r) => (
            <button
              key={r.id}
              onClick={() => setSelectedId(r.id)}
              className={`w-full text-left rounded-lg border px-2.5 py-2 transition ${
                selectedId === r.id
                  ? 'border-cyan-500/50 bg-cyan-500/10'
                  : 'border-slate-800 bg-slate-900/50 hover:border-slate-700'
              }`}
            >
              <div className="flex items-center gap-2">
                <span className={`text-[10px] font-bold px-1.5 py-0.5 rounded ${STATUS_STYLE[r.status] || STATUS_STYLE.queued}`}>
                  {r.status}
                </span>
                <span className="text-[10px] text-slate-500 ml-auto">{relTime(r.created_at)}</span>
              </div>
              <div className="text-xs text-slate-200 truncate mt-1" title={r.signature}>
                {r.signature || '(no signature)'}
              </div>
              <div className="text-[10px] text-orange-300 font-mono truncate">{short(r.file, 36)}</div>
              <div className="text-[10px] text-slate-500 truncate">{shortRepo(r.url)}</div>
            </button>
          ))}
        </div>
      </div>

      <div className="flex-1 min-w-0 flex flex-col min-h-0 border border-slate-800 rounded-lg bg-slate-900/40">
        {!selectedId ? (
          <div className="flex-1 flex items-center justify-center text-slate-600 text-sm">
            Select a review to see its assessment.
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 px-3 py-2 border-b border-slate-800">
              <span className={`text-[10px] font-bold px-1.5 py-0.5 rounded ${STATUS_STYLE[detail?.status] || STATUS_STYLE.queued}`}>
                {detail?.status || 'loading'}
              </span>
              <span className="text-xs text-slate-300 truncate flex-1" title={detail?.signature}>
                {detail?.signature || ''}
              </span>
              {streaming && (
                <span className="text-[10px] text-cyan-300 flex items-center gap-1">
                  <span className="w-1.5 h-1.5 rounded-full bg-cyan-400 animate-pulse" />
                  streaming
                </span>
              )}
              <button
                onClick={() => setSelectedId(null)}
                className="p-1 hover:bg-slate-800 rounded text-slate-400"
              >
                <X size={15} />
              </button>
              <button
                onClick={() => remove(selectedId)}
                className="p-1 hover:bg-slate-800 rounded text-slate-400 hover:text-red-400"
                title="Delete review"
              >
                <Trash2 size={15} />
              </button>
            </div>

            <div className="flex-1 overflow-y-auto p-3 space-y-4 min-h-0">
              {detail && (
                <div className="grid grid-cols-2 gap-2 text-[11px] text-slate-400">
                  <div className="truncate" title={detail.repo || detail.url}>{shortRepo(detail.repo || detail.url)}</div>
                  <div className="truncate text-right">{detail.provider} · {detail.model}</div>
                </div>
              )}

              {detail?.error && (
                <div className="text-xs text-red-300 bg-red-500/10 border border-red-500/30 rounded p-2 whitespace-pre-wrap">
                  {detail.error}
                </div>
              )}

              {assessment ? (
                <MarkdownLite text={assessment} />
              ) : (
                <div className="text-slate-600 italic text-sm">
                  {streaming ? 'Waiting for the model\u2026' : 'No assessment yet.'}
                </div>
              )}

              {messages.length > 0 && (
                <div className="border-t border-slate-800 pt-3">
                  <h4 className="text-xs uppercase tracking-wide text-slate-500 mb-2 flex items-center gap-1.5">
                    <MessageSquare size={13} /> Follow-up
                  </h4>
                  <div ref={chatScrollRef} className="space-y-2 max-h-64 overflow-y-auto">
                    {messages.map((msg, i) => (
                      <div
                        key={i}
                        className={`text-xs rounded p-2 whitespace-pre-wrap ${
                          msg.role === 'user'
                            ? 'bg-slate-800/70 text-slate-200 ml-8'
                            : 'bg-slate-950/70 text-slate-300 mr-8'
                        }`}
                      >
                        {msg.content}
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>

            <div className="border-t border-slate-800 p-2 flex items-center gap-2">
              <input
                value={chat}
                onChange={(e) => setChat(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && sendChat()}
                placeholder="Ask a follow-up question…"
                className="flex-1 bg-slate-950 border border-slate-700 rounded px-3 py-1.5 text-sm focus:outline-none focus:border-cyan-500"
              />
              <button
                onClick={sendChat}
                disabled={chatBusy || !chat.trim()}
                className="p-2 rounded bg-cyan-600 hover:bg-cyan-500 disabled:opacity-40 text-white"
              >
                {chatBusy ? <RefreshCw size={15} className="animate-spin" /> : <Send size={15} />}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
