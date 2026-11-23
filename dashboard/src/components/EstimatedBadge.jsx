// Every financial figure in the UI is a list-price estimate, never a bill.
// This badge is deliberately loud (amber) so it cannot be missed.
export default function EstimatedBadge({ className = '' }) {
  return (
    <span
      title="Calculated from published list prices. Not a real invoice."
      className={`inline-flex items-center rounded border border-amber-400/50 bg-amber-400/10 px-1.5 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-amber-300 ${className}`}
    >
      estimated
    </span>
  );
}
