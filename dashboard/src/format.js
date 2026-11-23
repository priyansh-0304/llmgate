export function fmtUSD(v) {
  if (!Number.isFinite(v)) return '$0.00';
  return v >= 1 ? `$${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}` : `$${v.toFixed(4)}`;
}

export function fmtInt(v) {
  return Math.round(v || 0).toLocaleString();
}

export function fmtLatency(ms) {
  if (!Number.isFinite(ms) || ms <= 0) return '0 ms';
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms.toFixed(0)} ms`;
}
