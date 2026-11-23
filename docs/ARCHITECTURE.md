# Architecture

Diagrams are Mermaid, so they render on GitHub. PNG copies are next to this file.

## 1. What happens to one request

Follow `POST /v1/chat/completions` from top to bottom. Red boxes are early exits.
Code: `internal/middleware/middleware.go` (steps 1-3) and `internal/proxy/proxy.go` (everything after).

```mermaid
flowchart TD
  C(["<b>Client</b><br/>POST /v1/chat/completions"]):::edge --> MW["<b>Recover + Instrument</b> wrap everything<br/>catch crashes · start timer · count in-flight"]
  MW --> AUTH{"Valid API key?"}
  AUTH -- no --> E401[/"401 Unauthorized"/]:::err
  AUTH -- yes --> RL{"Token left in the bucket?<br/>(Lua script in Redis)"}
  RL -- "bucket empty" --> E429[/"429 Too Many Requests<br/>+ Retry-After"/]:::err
  RL -- "Redis down, fail-open off" --> E503[/"503 limiter unavailable"/]:::err
  RL -- "yes<br/>(or Redis down, fail-open on)" --> PARSE["Read body (max 1 MiB) · parse JSON<br/>pick route · rewrite model alias"]
  PARSE -- "bad JSON / too big" --> E400[/"400 / 413"/]:::err
  PARSE --> CACHEABLE{"Not streaming AND<br/>temperature = 0?"}
  CACHEABLE -- yes --> LOOKUP{"Cache lookup<br/>key = SHA-256 of route + body"}
  LOOKUP -- HIT --> HIT["Return saved answer<br/>X-Cache: HIT"]:::good
  CACHEABLE -- "no (BYPASS)" --> FWD
  LOOKUP -- MISS --> FWD["Forward to Ollama or mock<br/>client key is never forwarded"]
  FWD -- "unreachable / timeout" --> E502[/"502 Bad Gateway"/]:::err
  FWD -- "streaming (SSE)" --> STREAM["Relay chunk by chunk, flush each one<br/>read token usage from the last chunk"]
  FWD -- "normal JSON" --> BUF["Pass reply + status code through<br/>if 200 and cacheable: save in cache (TTL)"]
  HIT --> REC
  STREAM --> REC
  BUF --> REC
  E502 --> REC
  REC["<b>Record</b><br/>tokens (upstream usage, else chars / 4) · latency<br/>request count · estimated cost per commercial model<br/>in-memory stats for the dashboard"]:::rec --> OUT(["<b>Response to client</b><br/>every response, errors too,<br/>is also counted in gateway_http_requests_total"]):::edge

  classDef err fill:#3b1d1d,stroke:#f87171,color:#fecaca
  classDef good fill:#12332a,stroke:#34d399,color:#a7f3d0
  classDef rec fill:#2b2a1a,stroke:#fbbf24,color:#fef3c7
  classDef edge fill:#1e293b,stroke:#94a3b8,color:#e2e8f0
```

## 2. System architecture

Code and config: `cmd/gateway`, `deploy/k8s/*.yaml`, `docker-compose.yml`.

```mermaid
flowchart TB
  APP["Your app / curl / k6"]:::ext
  BR["Browser"]:::ext

  subgraph K["kind cluster (Kubernetes inside Docker)"]
    DB["<b>Dashboard</b><br/>nginx + React"]
    GR["<b>Grafana</b><br/>charts"]
    PR["<b>Prometheus</b><br/>stores metrics"]
    GW["<b>Gateway</b> (Go)<br/>auth · rate limit · cache · routing<br/>token + cost accounting<br/>/metrics · /api/stats"]
    R[("<b>Redis</b><br/>rate-limit buckets<br/>cached answers")]
    OL["<b>Ollama</b><br/>quantized model on CPU"]
    MK["<b>Mock upstream</b><br/>fake OpenAI server"]
    PV[("Volume<br/>model files")]
    CFG["<b>Config</b><br/>Secret: API key, passwords<br/>ConfigMap: limits, URLs, model<br/>pricing.json: reference prices"]
  end

  APP == "chat requests :8080" ==> GW
  APP -. "k6 baseline :9000" .-> MK
  BR -- ":3000" --> DB
  BR -- ":3001" --> GR
  DB -- "/api/stats<br/>(nginx proxy, no key in browser)" --> GW
  GR -- "PromQL queries" --> PR
  PR -- "scrapes /metrics every 5 s" --> GW
  GW <--> R
  GW -- "route: ollama" --> OL
  GW -- "route: mock" --> MK
  OL --- PV
  GW -. "reads" .-> CFG
  R -. "password" .-> CFG
  GR -. "password" .-> CFG

  classDef ext fill:#0f172a,stroke:#94a3b8,color:#e2e8f0,stroke-dasharray:4 3
  classDef core fill:#12332a,stroke:#34d399,color:#d1fae5
  classDef store fill:#1e293b,stroke:#60a5fa,color:#dbeafe
  classDef model fill:#2a2438,stroke:#a78bfa,color:#ede9fe
  classDef obs fill:#2b2a1a,stroke:#fbbf24,color:#fef3c7
  classDef cfg fill:#1e293b,stroke:#64748b,color:#cbd5e1
  class GW core
  class R,PV store
  class OL,MK model
  class PR,GR,DB obs
  class CFG cfg
```

## 3. System-design concepts and where they live

| Concept | Where in the code | What it does here |
|---|---|---|
| API gateway / reverse proxy | `internal/proxy/proxy.go` | Single entry point; routes to Ollama or mock, rewrites model alias, never forwards client credentials |
| Middleware chain | `internal/middleware/middleware.go` (`Chain`) | Recover, Instrument, Auth, RateLimit wrap the handler like onion layers |
| Rate limiting (token bucket) | `internal/ratelimit/ratelimit.go`, `redis.go` | Burst of N, sustained R/sec per API key or IP; 429 + `Retry-After` |
| Atomic shared state | `redis.go` (Lua script) | Check-and-spend runs inside Redis, so replicas cannot both take the last token |
| Caching (TTL, deterministic only) | `internal/cache/cache.go`, `proxy.go` | Key = SHA-256 of route + body; only non-streaming `temperature=0`; failures count as a miss |
| Cache eviction | `deploy/k8s/10-redis.yaml` | `maxmemory 128mb`, `allkeys-lru`, no persistence |
| Authentication | `middleware.Auth`, `clientKey` | Constant-time key compare; the key is hashed before use as a Redis key |
| Observability: metrics | `internal/metrics/metrics.go` | Counters, histograms (p95/p99), gauge; Prometheus **pull** model, 5 s scrape (`deploy/k8s/files/prometheus.yml`) |
| Observability: dashboards, logs, health | Grafana provisioning in `deploy/k8s/files/`, `slog` JSON logs, `/healthz` + probes in `40-gateway.yaml` | Visualise, debug, let Kubernetes restart a sick pod |
| Metric cardinality control | `Route.KnownModels` in `proxy.go`, `pathLabel` in middleware | Unknown model names / paths collapse to `other` so a client cannot create unlimited time series |
| Graceful degradation | `RATE_FAIL_OPEN` in `config.go`; cache errors; `main.go`; `dashboard/src/useStats.js` | Redis outage does not take the gateway down; dashboard falls back to labelled demo data |
| Timeouts and size limits | `proxy.go` transport, `main.go` server, `MaxBytesReader`, `LimitReader` | Bound waiting and memory; deliberately no write timeout so streams are not cut |
| Streaming (SSE) | `proxy.go` `relayStream` | Flushes each event; reads token usage from the final chunk |
| Graceful shutdown | `cmd/gateway/main.go` | On Ctrl+C / pod stop, finish in-flight requests |
| 12-factor config | `internal/config/config.go` | All settings from env vars, injected by ConfigMap / Secret |
| Interfaces / dependency inversion | `ratelimit.Limiter`, `cache.Cache` | Same handler runs on Redis or in-memory; makes tests fast |
| Secrets management | `scripts/gen-secrets.sh`, `secret.example.yaml`, `secretKeyRef` | Random local secrets, git-ignored, injected as env vars, absent from images and the browser |
| Same-origin proxy (BFF-style) | `dashboard/nginx.conf.template` | Browser calls `/api/stats` on its own origin; nginx forwards to the gateway, so no key or CORS |
| Containerisation | `deploy/Dockerfile.app` | Multi-stage build, distroless, non-root |
| Orchestration | `deploy/k8s/*.yaml`, `kind-config.yaml` | Deployments, Services, ConfigMap/Secret, PVC for model files, one-off Job to pull the model, resource limits, read-only root FS, ports bound to 127.0.0.1 |
| Load testing | `loadtest/k6.js`, `cmd/mock-upstream` | Constant-arrival-rate (open model) so slow responses do not hide overload; thresholds on p95/p99 |

## 4. Deliberately not included

Single gateway replica (`/api/stats` is in memory), no retries or circuit breaker, no fallback between upstreams or load balancing across several, no per-user quotas or database, no TLS, no autoscaling, no distributed tracing. Each is a reasonable next step.
