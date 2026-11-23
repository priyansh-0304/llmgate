// One pill per upstream route, coloured by its circuit-breaker state.
const LOOK = {
  closed: { dot: 'bg-emerald-400', text: 'text-emerald-300', label: 'healthy' },
  'half-open': { dot: 'bg-amber-400', text: 'text-amber-300', label: 'testing recovery' },
  open: { dot: 'bg-red-400', text: 'text-red-300', label: 'circuit open' },
};

export default function UpstreamHealth({ routes }) {
  if (!routes?.length) return null; // older gateway without this field

  return (
    <section
      aria-label="Upstream health"
      className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl border border-slate-700/70 bg-slate-800/50 px-5 py-3"
    >
      <h2 className="text-xs font-medium uppercase tracking-wider text-slate-400">Upstreams</h2>
      {routes.map((r) => {
        const look = LOOK[r.circuit] ?? LOOK.closed;
        return (
          <p key={r.name} className="flex items-center gap-2 font-mono text-sm">
            <span className={`h-2.5 w-2.5 rounded-full ${look.dot}`} aria-hidden="true" />
            <span className="text-slate-100">{r.name}</span>
            <span className={look.text}>{look.label}</span>
            {r.is_fallback && (
              <span className="rounded border border-slate-600 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-slate-400">
                fallback
              </span>
            )}
          </p>
        );
      })}
    </section>
  );
}
