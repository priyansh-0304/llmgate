// Synthetic traffic for when the gateway is unreachable. Seeded from
// data/demo-stats.seed.json. These numbers are NOT measurements; the UI
// shows a "demo data" banner whenever this generator is in use.
import pricing from '@data/pricing.seed.json';
import seed from '@data/demo-stats.seed.json';

const rand = (lo, hi) => lo + Math.random() * (hi - lo);

export function costFor(ref, promptTokens, completionTokens) {
  return (
    (promptTokens / 1e6) * ref.input_per_mtok_usd +
    (completionTokens / 1e6) * ref.output_per_mtok_usd
  );
}

export function createDemo() {
  const s = { ...seed.start };
  const startedAt = Date.now();
  let lastSec = Math.floor(Date.now() / 1000);
  let latencySumMs = s.avg_latency_ms * s.total_requests;
  let series = seed.throughput_seed.map((tokens, i, arr) => ({
    t: lastSec - (arr.length - 1 - i),
    tokens,
  }));

  // Returns a snapshot shaped exactly like GET /api/stats.
  return function tick() {
    const nowSec = Math.floor(Date.now() / 1000);
    const elapsed = Math.max(1, nowSec - lastSec);
    lastSec = nowSec;

    for (let sec = 0; sec < elapsed; sec++) {
      const n = Math.round(rand(...seed.tick.requests_per_second));
      let tokens = 0;
      for (let i = 0; i < n; i++) {
        const p = Math.round(rand(...seed.tick.prompt_tokens_per_request));
        const c = Math.round(rand(...seed.tick.completion_tokens_per_request));
        s.prompt_tokens += p;
        s.completion_tokens += c;
        tokens += p + c;
        latencySumMs += rand(...seed.tick.latency_ms);
        if (Math.random() < seed.tick.cache_hit_rate) s.cache_hits += 1;
        if (Math.random() < seed.tick.rate_limited_rate) s.rate_limited += 1;
      }
      s.total_requests += n;
      series = [...series.slice(1), { t: nowSec - (elapsed - 1 - sec), tokens }];
    }

    return {
      mode: 'demo',
      estimated: true,
      pricing_as_of: pricing.as_of,
      disclaimer: pricing.disclaimer,
      default_reference: pricing.default_reference,
      total_requests: s.total_requests,
      cache_hits: s.cache_hits,
      rate_limited: s.rate_limited,
      prompt_tokens: s.prompt_tokens,
      completion_tokens: s.completion_tokens,
      avg_latency_ms: latencySumMs / s.total_requests,
      uptime_seconds: (Date.now() - startedAt) / 1000,
      routes: [
        { name: 'mock', circuit: 'closed', is_fallback: true },
        { name: 'ollama', circuit: 'closed', is_fallback: false },
      ],
      references: pricing.references.map((r) => ({
        ...r,
        estimated_cost_usd: costFor(r, s.prompt_tokens, s.completion_tokens),
      })),
      throughput: series,
    };
  };
}
