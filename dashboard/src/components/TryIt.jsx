import { useState } from 'react';

// Sends a REAL request through the gateway so the numbers above move.
// The browser never holds a key: POST /api/demo makes the gateway add its own.
export default function TryIt() {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState(null);

  async function send(mode) {
    setBusy(true);
    const started = performance.now();
    try {
      const res = await fetch(`/api/demo?mode=${mode}`, { method: 'POST' });
      const ms = Math.round(performance.now() - started);
      if (res.status === 404) {
        setResult({ tone: 'warn', text: 'The test endpoint is off. Start the gateway with DEMO_ENDPOINT=true.' });
      } else {
        const cache = res.headers.get('X-Cache');
        const route = res.headers.get('X-Gateway-Route');
        const parts = [`HTTP ${res.status}`, route && `route ${route}`, cache && `cache ${cache}`, `${ms} ms`].filter(Boolean);
        const tone = res.ok ? 'ok' : 'err';
        const hint = res.status === 429 ? ' (rate limit or token budget: that is the gateway working)' : '';
        setResult({ tone, text: parts.join(' · ') + hint });
      }
    } catch {
      setResult({ tone: 'err', text: 'Could not reach the gateway.' });
    } finally {
      setBusy(false);
    }
  }

  const tones = { ok: 'text-emerald-300', warn: 'text-amber-300', err: 'text-red-300' };
  const btn =
    'rounded-md border border-slate-600 bg-slate-900 px-3 py-1.5 font-mono text-xs text-slate-100 hover:border-emerald-400 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-emerald-400';

  return (
    <section
      aria-label="Send a test request"
      className="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-xl border border-slate-700/70 bg-slate-800/50 px-5 py-3"
    >
      <h2 className="text-xs font-medium uppercase tracking-wider text-slate-400">Try it</h2>
      <button type="button" className={btn} disabled={busy} onClick={() => send('new')}>
        Send test request
      </button>
      <button type="button" className={btn} disabled={busy} onClick={() => send('repeat')}>
        Send same request (cache)
      </button>
      <p role="status" className={`min-w-0 font-mono text-xs ${result ? tones[result.tone] : 'text-slate-400'}`}>
        {busy ? 'Sending…' : result ? result.text : 'Real requests through the gateway. Watch the cards above move.'}
      </p>
    </section>
  );
}