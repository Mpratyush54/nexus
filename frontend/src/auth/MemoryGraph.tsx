/**
 * Decorative: six agents feeding one shared memory. Pure SVG + CSS animation
 * (auth.css), so it costs no script time and stops under reduced motion.
 */
const AGENTS = [
  { name: 'Claude Code', color: '#d9895b', x: 120, y: 150 },
  { name: 'Codex', color: '#9aa7f0', x: 530, y: 125 },
  { name: 'Cursor', color: '#bfc5c2', x: 600, y: 300 },
  { name: 'OpenCode', color: '#7fc2a6', x: 500, y: 475 },
  { name: 'Gemini', color: '#7aa8f0', x: 110, y: 470 },
  { name: 'Kimi', color: '#c59ae6', x: 50, y: 310 },
]
const CORE = { x: 350, y: 300 }

/** Small memory nodes drifting around the core, like captured decisions. */
const MEMORIES = [
  { x: 270, y: 215, r: 4 },
  { x: 440, y: 230, r: 3 },
  { x: 455, y: 375, r: 4 },
  { x: 255, y: 390, r: 3 },
  { x: 350, y: 180, r: 3 },
  { x: 350, y: 425, r: 3 },
]

function curve(a: { x: number; y: number }, b: { x: number; y: number }, bend = 0.18) {
  const mx = (a.x + b.x) / 2
  const my = (a.y + b.y) / 2
  const dx = b.x - a.x
  const dy = b.y - a.y
  return `M${a.x} ${a.y} Q${mx - dy * bend} ${my + dx * bend} ${b.x} ${b.y}`
}

export function MemoryGraph() {
  return (
    <div className="au-graph" aria-hidden>
      <svg viewBox="20 135 720 420" preserveAspectRatio="xMidYMid meet">
        <defs>
          <radialGradient id="au-core-glow" cx="50%" cy="50%" r="50%">
            <stop offset="0" stopColor="#e8bb67" stopOpacity="0.18" />
            <stop offset="1" stopColor="#e8bb67" stopOpacity="0" />
          </radialGradient>
        </defs>
        <g transform="translate(0 50)">
          <circle cx={CORE.x} cy={CORE.y} r="150" fill="url(#au-core-glow)" />
          {AGENTS.map((a, i) => (
            <g key={a.name}>
              <path className="g-edge" d={curve(a, CORE)} />
              <path
                className="g-pulse"
                d={curve(a, CORE)}
                pathLength={400}
                style={{ animationDelay: `${i * 0.85}s` }}
              />
            </g>
          ))}
          {MEMORIES.map((m, i) => (
            <g key={i} className="g-float" style={{ animationDelay: `${i * -1.4}s` }}>
              <path className="g-edge" d={`M${CORE.x} ${CORE.y} L${m.x} ${m.y}`} strokeDasharray="2 4" />
              <circle cx={m.x} cy={m.y} r={m.r} fill="#3f4540" />
            </g>
          ))}
          <circle className="g-halo" cx={CORE.x} cy={CORE.y} r="58" />
          <circle className="g-core" cx={CORE.x} cy={CORE.y} r="44" />
          <g transform={`translate(${CORE.x - 24} ${CORE.y - 24}) scale(.75)`}>
            <g transform="translate(5 5) scale(.84375)">
              <path d="M14 47V20a6 6 0 0 1 6-6h5l25 33" stroke="#F4F1E9" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" fill="none" />
              <path d="M50 17v27a6 6 0 0 1-6 6h-5L14 17" stroke="#E8BB67" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" fill="none" />
              <path d="m25 32 8 10" stroke="#F4F1E9" strokeWidth="8" strokeLinecap="round" />
            </g>
          </g>
          {AGENTS.map((a) => (
            <g key={`n-${a.name}`} className="g-float">
              <rect className="g-node" x={a.x - 10} y={a.y - 21} width={a.name.length * 12 + 52} height={42} rx={10} />
              <circle className="g-dot" cx={a.x + 10} cy={a.y} r={6} fill={a.color} />
              <text className="g-label" x={a.x + 26} y={a.y + 6.5}>
                {a.name}
              </text>
            </g>
          ))}
        </g>
      </svg>
    </div>
  )
}
