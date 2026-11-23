// k6 load test for the mock upstream or the gateway.
//
//   Baseline (upstream only, no limiter involved):
//     k6 run -e TARGET=http://localhost:9000 loadtest/k6.js
//   Through the gateway (raise RATE_CAPACITY first, see README):
//     k6 run -e TARGET=http://localhost:8080 -e API_KEY=<key> loadtest/k6.js
//
// Everything is local: no paid API is contacted, so no real 429s.
import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

const TARGET = __ENV.TARGET || 'http://localhost:8080';
const API_KEY = __ENV.API_KEY || '';
const MODEL = __ENV.MODEL || 'mock-fast';
const RATE = Number(__ENV.RATE || 100); // requests per second
const DURATION = __ENV.DURATION || '60s';
const CACHE_RATIO = Number(__ENV.CACHE_RATIO || 0.3); // share of repeat (cacheable) prompts

const rateLimited = new Rate('gateway_rate_limited'); // share of 429 responses
const cacheHits = new Counter('gateway_cache_hits'); // responses with X-Cache: HIT

export const options = {
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate', // fixed request rate, independent of response time
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 50,
      maxVUs: 1000,
    },
  },
  // Latency thresholds only look at successful (HTTP 200) responses.
  thresholds: {
    'http_req_duration{expected_response:true}': ['p(95)<500', 'p(99)<1000'],
    http_req_failed: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

// Treat 429 as "expected" so it is counted by gateway_rate_limited instead of
// being lumped in with real failures.
http.setResponseCallback(http.expectedStatuses(200, 429));

export default function () {
  const repeat = Math.random() < CACHE_RATIO;
  const payload = JSON.stringify({
    model: MODEL,
    // temperature 0 + identical prompt => the gateway can serve it from cache
    temperature: repeat ? 0 : 0.7,
    messages: [{ role: 'user', content: repeat ? 'What is a token bucket?' : `unique prompt ${__VU}-${__ITER}` }],
  });
  const headers = { 'Content-Type': 'application/json' };
  if (API_KEY) headers.Authorization = `Bearer ${API_KEY}`;

  const res = http.post(`${TARGET}/v1/chat/completions`, payload, { headers });

  rateLimited.add(res.status === 429);
  if (res.headers['X-Cache'] === 'HIT') cacheHits.add(1);
  check(res, { 'status is 200 or 429': (r) => r.status === 200 || r.status === 429 });
}
