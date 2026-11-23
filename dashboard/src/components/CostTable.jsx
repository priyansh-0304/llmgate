import EstimatedBadge from './EstimatedBadge.jsx';
import { fmtUSD } from '../format.js';

// "What would this exact traffic cost on each commercial API?"
export default function CostTable({ references, selectedId }) {
  return (
    <section className="rounded-xl border border-slate-700/70 bg-slate-800/50 p-5">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-xs font-medium uppercase tracking-wider text-slate-400">
          Same traffic, commercial list prices
        </h2>
        <EstimatedBadge />
      </div>

      <div className="mt-4 overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-slate-700 text-xs uppercase tracking-wider text-slate-500">
              <th scope="col" className="pb-2 pr-2 font-medium">Model</th>
              <th scope="col" className="pb-2 pr-2 text-right font-medium">In/1M</th>
              <th scope="col" className="pb-2 pr-2 text-right font-medium">Out/1M</th>
              <th scope="col" className="pb-2 text-right font-medium">Est. cost</th>
            </tr>
          </thead>
          <tbody className="font-mono tabular-nums">
            {references.map((r) => {
              const active = r.id === selectedId;
              return (
                <tr key={r.id} className={`border-b border-slate-700/50 last:border-0 ${active ? 'bg-emerald-400/10' : ''}`}>
                  <td className="py-2.5 pr-2 font-sans">
                    <span className={`block ${active ? 'text-emerald-300' : 'text-slate-200'}`}>{r.model}</span>
                    <span className="block text-xs text-slate-500">{r.provider}</span>
                  </td>
                  <td className="py-2.5 pr-2 text-right text-slate-300">${r.input_per_mtok_usd.toFixed(2)}</td>
                  <td className="py-2.5 pr-2 text-right text-slate-300">${r.output_per_mtok_usd.toFixed(2)}</td>
                  <td className={`py-2.5 text-right ${active ? 'text-emerald-400' : 'text-slate-200'}`}>{fmtUSD(r.estimated_cost_usd)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
