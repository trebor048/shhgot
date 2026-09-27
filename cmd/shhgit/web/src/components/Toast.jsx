import React, { createContext, useCallback, useContext, useState } from 'react'
import { AlertTriangle, CheckCircle2, Info, X } from 'lucide-react'

const ToastCtx = createContext(() => {})

export function useToast() {
  return useContext(ToastCtx)
}

const KINDS = {
  ok: { cls: 'border-green-500/40 bg-green-500/10 text-green-200', Icon: CheckCircle2 },
  error: { cls: 'border-red-500/40 bg-red-500/10 text-red-200', Icon: AlertTriangle },
  info: { cls: 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100', Icon: Info },
}

export function ToastProvider({ children }) {
  const [items, setItems] = useState([])

  const push = useCallback((text, kind = 'info') => {
    const id = Math.random().toString(36).slice(2)
    setItems((prev) => prev.concat([{ id, text, kind }]))
    setTimeout(() => {
      setItems((prev) => prev.filter((t) => t.id !== id))
    }, 4000)
  }, [])

  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-[100] space-y-2 w-[min(92vw,520px)]">
        {items.map((t) => {
          const k = KINDS[t.kind] || KINDS.info
          return (
            <div
              key={t.id}
              className={`flex items-start gap-2 px-3 py-2 rounded-lg border backdrop-blur shadow-lg ${k.cls}`}
            >
              <k.Icon size={16} className="mt-0.5 shrink-0" />
              <span className="text-sm flex-1 break-words">{t.text}</span>
              <button
                onClick={() => setItems((prev) => prev.filter((x) => x.id !== t.id))}
                className="opacity-60 hover:opacity-100"
              >
                <X size={14} />
              </button>
            </div>
          )
        })}
      </div>
    </ToastCtx.Provider>
  )
}
