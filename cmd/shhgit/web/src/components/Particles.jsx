import { useMemo } from 'react'

// Floating background particles.
//
// They animate only `transform` and `opacity`, so the browser keeps them on the
// compositor: no canvas, no requestAnimationFrame, no per-frame JavaScript. The
// count is deliberately small, and the whole layer is dropped for anyone who
// prefers reduced motion (see index.css). The random offsets are computed once
// and memoised so a re-render never reshuffles them.
const COUNT = 26
const HUES = ['34, 211, 238', '34, 197, 94', '148, 163, 184']

export default function Particles() {
  const particles = useMemo(
    () =>
      Array.from({ length: COUNT }, (_, i) => ({
        id: i,
        left: Math.random() * 100,
        size: 1 + Math.random() * 2.5,
        duration: 16 + Math.random() * 20,
        delay: -Math.random() * 36,
        hue: HUES[i % HUES.length],
      })),
    [],
  )

  return (
    <div className="particles" aria-hidden="true">
      {particles.map((p) => (
        <span
          key={p.id}
          className="particle"
          style={{
            left: `${p.left}%`,
            width: `${p.size}px`,
            height: `${p.size}px`,
            background: `rgba(${p.hue}, 0.55)`,
            animationDuration: `${p.duration}s`,
            animationDelay: `${p.delay}s`,
          }}
        />
      ))}
    </div>
  )
}
