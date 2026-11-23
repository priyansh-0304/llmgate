import { useState } from 'react';
import { useStats } from './useStats.js';
import { fmtInt, fmtLatency, fmtUSD } from './format.js';
import KpiCard from './components/KpiCard.jsx';
import ThroughputChart from './components/ThroughputChart.jsx';
import CostTable from './components/CostTable.jsx';
import UpstreamHealth from './components/UpstreamHealth.jsx';
import TryIt from './components/TryIt.jsx';

export default function App() {
  const { data, offline } = useStats();
  const [picked, setPicked] = useState(null);

  if (!data) {
    return (
      <main className="mx-auto max-w-6xl p-6">
        <p className="font-mono text-sm text-slate-400">{offline ? 'Gateway unreachable (demo fallback disabled).' : 'Connecting to gateway…'}</p>
      </main>
    );
  }

  const refs = data.references;
  const selected = refs.find((r) => r.id === (picked ?? data.default_reference)) ?? refs[0];
  const isDemo = data.mode === 'demo';
  const cacheShare = data.total_requests ? Math.round((data.cache_hits / data.total_requests) * 100) : 0;

  return (
    <main className="mx-auto max-w-6xl px-4 py-8 sm:px-6">
      <header className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="font-mono text-xl font-semibold tracking-tight text-slate-100">
            llmgate<span className="text-emerald-400">/</span>cost
          </h1>
          <p className="mt-1 text-sm text-slate-400">Local LLM gateway · token usage and estimated savings</p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <a
            href="/"
            className="rounded-md border border-slate-600 px-3 py-1.5 font-mono text-xs text-slate-300 hover:border-emerald-400 hover:text-emerald-300 focus-visible:outline-2 focus-visible:outline-emerald-400"
          >
            ← Guide
          </a>
          <span
            className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 font-mono text-xs ${
              isDemo ? 'border-amber-400/50 text-amber-300' : offline ? 'border-red-400/50 text-red-300' : 'border-emerald-400/40 text-emerald-300'
            }`}
          >
            <span className={`h-2 w-2 rounded-full ${isDemo ? 'bg-amber-400' : offline ? 'bg-red-400' : 'bg-emerald-400'}`} />
            {isDemo ? 'DEMO DATA' : offline ? 'OFFLINE' : 'LIVE'}
          </span>

          <label className="flex items-center gap-2 text-xs text-slate-400">
            Compare with
            <select
              value={selected.id}
              onChange={(e) => setPicked(e.target.value)}
              className="rounded-md border border-slate-600 bg-slate-900 px-2 py-1.5 font-mono text-xs text-slate-100 focus:outline-2 focus:outline-emerald-400"
            >
              {refs.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.provider} · {r.model}
                </option>
              ))}
            </select>
          </label>
        </div>
      </header>

      {isDemo && (
        <p role="status" className="mt-5 rounded-lg border border-amber-400/40 bg-amber-400/10 px-4 py-2.5 text-sm text-amber-200">
          The gateway is not reachable, so these are <strong>synthetic demo numbers</strong>, not measurements.
        </p>
      )}

      <div className="mt-6 grid gap-4 md:grid-cols-3">
        <KpiCard
          label="Total requests"
          value={fmtInt(data.total_requests)}
          sub={`${cacheShare}% served from cache · ${fmtInt(data.rate_limited)} rate-limited`}
        />
        <KpiCard
          label="Cost saved"
          estimated
          value={fmtUSD(selected.estimated_cost_usd)}
          sub={`vs ${selected.provider} ${selected.model} list price for ${fmtInt(data.prompt_tokens)} in / ${fmtInt(data.completion_tokens)} out tokens. Local cost counted as $0.`}
        />
        <KpiCard
          label="Average latency"
          value={fmtLatency(data.avg_latency_ms)}
          sub="End-to-end through the gateway, including cache hits."
        />
      </div>

      <div className="mt-4">
        <UpstreamHealth routes={data.routes} />
      </div>
      
      <div className="mt-4">
        <TryIt />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-5">
        <div className="min-w-0 lg:col-span-3">
          <ThroughputChart points={data.throughput} />
        </div>
        <div className="min-w-0 lg:col-span-2">
          <CostTable references={refs} selectedId={selected.id} />
        </div>
      </div>

      <footer className="mt-6 text-xs leading-relaxed text-slate-500">
        <p>
          <span className="font-mono text-amber-300/80">estimated</span> · Prices as of {data.pricing_as_of}. {data.disclaimer}
        </p>
        <p className="mt-2">
          “Saved” assumes a commercial model would have handled the same tokens; it says nothing about answer quality, and a small local model is not equivalent to a frontier one.
        </p>
      </footer>
    </main>
  );
}
