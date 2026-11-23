import { useEffect, useRef, useState } from 'react';
import { createDemo } from './demo.js';

const POLL_MS = 2000;
const FAILURES_BEFORE_DEMO = 2;

// ?demo=1 forces demo data; ?demo=0 never falls back to it (shows "offline").
const demoParam = new URLSearchParams(window.location.search).get('demo');

export function useStats() {
  const [data, setData] = useState(null);
  const [offline, setOffline] = useState(false);
  const demo = useRef(null);
  const failures = useRef(0);

  useEffect(() => {
    let cancelled = false;

    async function poll() {
      let next = null;

      if (demoParam !== '1') {
        const ctl = new AbortController();
        const timer = setTimeout(() => ctl.abort(), 1500);
        try {
          const res = await fetch('/api/stats', { signal: ctl.signal, cache: 'no-store' });
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          next = { ...(await res.json()), mode: 'live' };
          failures.current = 0;
        } catch {
          failures.current += 1;
        } finally {
          clearTimeout(timer);
        }
      }

      if (!next && demoParam !== '0' && (demoParam === '1' || failures.current >= FAILURES_BEFORE_DEMO)) {
        demo.current ??= createDemo();
        next = demo.current();
      }

      if (cancelled) return;
      if (next) {
        setData(next);
        setOffline(false);
      } else if (failures.current >= FAILURES_BEFORE_DEMO) {
        setOffline(true);
      }
    }

    poll();
    const id = setInterval(poll, POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  return { data, offline };
}
