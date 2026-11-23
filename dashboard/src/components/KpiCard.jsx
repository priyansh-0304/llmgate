import EstimatedBadge from './EstimatedBadge.jsx';

export default function KpiCard({ label, value, sub, estimated = false }) {
  return (
    <section className="rounded-xl border border-slate-700/70 bg-slate-800/50 p-5">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-xs font-medium uppercase tracking-wider text-slate-400">{label}</h2>
        {estimated && <EstimatedBadge />}
      </div>
      <p className="mt-3 font-mono text-3xl font-semibold tabular-nums text-emerald-400 sm:text-4xl">{value}</p>
      {sub && <p className="mt-2 text-xs leading-relaxed text-slate-400">{sub}</p>}
    </section>
  );
}
