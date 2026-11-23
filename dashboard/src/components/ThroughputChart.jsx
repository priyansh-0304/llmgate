// Hand-rolled SVG area chart: no chart library, so the bundle stays tiny.
const W = 720;
const H = 240;
const PAD = { l: 52, r: 12, t: 14, b: 26 };

function niceCeil(v) {
  if (v <= 10) return 10;
  const pow = 10 ** Math.floor(Math.log10(v));
  const n = v / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * pow;
}

export default function ThroughputChart({ points }) {
  const n = points.length;
  const peak = Math.max(0, ...points.map((p) => p.tokens));
  const top = niceCeil(peak);
  const last = n ? points[n - 1].tokens : 0;

  const x = (i) => PAD.l + (n <= 1 ? 0 : (i / (n - 1)) * (W - PAD.l - PAD.r));
  const y = (v) => PAD.t + (1 - v / top) * (H - PAD.t - PAD.b);

  const line = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(p.tokens).toFixed(1)}`).join(' ');
  const area = n
    ? `${line} L${x(n - 1).toFixed(1)},${y(0).toFixed(1)} L${x(0).toFixed(1)},${y(0).toFixed(1)} Z`
    : '';
  const grid = [0, 0.25, 0.5, 0.75, 1];

  return (
    <section className="rounded-xl border border-slate-700/70 bg-slate-800/50 p-5">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-xs font-medium uppercase tracking-wider text-slate-400">
          Token throughput · last {n}s
        </h2>
        <p className="font-mono text-xs tabular-nums text-slate-400">
          now <span className="text-emerald-400">{last.toLocaleString()}</span> tok/s · peak{' '}
          <span className="text-slate-200">{peak.toLocaleString()}</span>
        </p>
      </div>

      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="mt-4 h-auto w-full"
        role="img"
        aria-label={`Token throughput over the last ${n} seconds. Currently ${last} tokens per second, peak ${peak}.`}
      >
        <defs>
          <linearGradient id="tp-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="#34d399" stopOpacity="0.35" />
            <stop offset="100%" stopColor="#34d399" stopOpacity="0" />
          </linearGradient>
        </defs>

        {grid.map((g) => (
          <g key={g}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(top * g)} y2={y(top * g)} stroke="#334155" strokeWidth="1" strokeDasharray={g === 0 ? '0' : '3 4'} />
            <text x={PAD.l - 8} y={y(top * g) + 4} textAnchor="end" className="fill-slate-500 font-mono" fontSize="11">
              {Math.round(top * g).toLocaleString()}
            </text>
          </g>
        ))}

        <text x={PAD.l} y={H - 6} className="fill-slate-500 font-mono" fontSize="11">
          -{n}s
        </text>
        <text x={W - PAD.r} y={H - 6} textAnchor="end" className="fill-slate-500 font-mono" fontSize="11">
          now
        </text>

        {n > 1 && (
          <>
            <path d={area} fill="url(#tp-fill)" />
            <path d={line} fill="none" stroke="#34d399" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
            <circle cx={x(n - 1)} cy={y(last)} r="3.5" fill="#34d399" />
          </>
        )}
      </svg>
    </section>
  );
}
