# Analytics service

Read-only Go service for the staff commerce overview. It reads reviewed
ClickHouse serving views as a restricted identity and exposes one protected
endpoint. It owns no transactional data, migrations, gRPC service or UI.

The [canonical API contract](https://github.com/duynhlab/homelab/blob/main/docs/api/analytics.md)
owns routes, payloads, metric semantics and failure behavior; the
[telemetry contract](https://github.com/duynhlab/homelab/blob/main/docs/observability/analytics/README.md)
owns signal names and alert semantics. Platform manifests, gateway routing and
the Admin Portal page live in `duynhlab/homelab` and `duynhlab/admin-service`.

## Develop

Use Go 1.27.2. The layers are `internal/web/v1` (transport/auth),
`internal/logic/v1` (freshness and exact arithmetic),
`internal/core/repository` (bounded native ClickHouse reads) and
`internal/telemetry` (idle freshness sampler). See [AGENTS.md](./AGENTS.md).

```bash
go build ./...
go vet ./...
go test -race -count=1 ./...
bash scripts/test-integration.sh      # needs Docker: disposable ClickHouse 26.9
go -C tools build -o ../.bin/golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint
./.bin/golangci-lint run ./...
```

CI (`.github/workflows/check.yml` on PRs, `build.yml` on `main` and `v*` tags)
calls the pinned `duynhlab/gha-workflows` reusable workflows: Go check with the
fleet lint policy and the integration script above, gitleaks, Sonar, image
build with Trivy scan and cosign signature, and a GoReleaser binary on tags.
The published image is `ghcr.io/duynhlab/analytics-service/analytics-service`.

Unit tests use in-memory dependencies and an ephemeral local JWKS HTTP server.
The integration suite creates a unique `analytics_test_*` database and
restricted test user on a disposable ClickHouse, then drops both. It checks
parameter binding, native scans, UTC event/cohort boundaries, nullable mixed-type
order IDs, version deduplication and tombstones, money above JavaScript's safe
range, both ledger legs, product joins, read-only grants, invalid postings and
cancellation. It reproduces the reviewed view pattern; it is not proof of the
platform's grants or CDC behavior.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address |
| `CLICKHOUSE_ADDR` | required | `host:port` of the native protocol |
| `CLICKHOUSE_USER` | required | Must be `analytics_reader` |
| `CLICKHOUSE_PASSWORD` | required | Reader password |
| `CLICKHOUSE_TLS` | `true` | `false` turns ClickHouse TLS off; logged at start |
| `CLICKHOUSE_CA_FILE` | — | Private CA (PEM); only with `CLICKHOUSE_TLS=true` |
| `OIDC_STAFF_ISSUER` | required | Public realm URL ending in `/realms/duynhlab-staff` |
| `OIDC_STAFF_JWKS_URL` | derived from issuer | In-cluster fetch path; `http://` allowed only for `*.svc.cluster.local`, loopback or a Compose service name |
| `OIDC_AUDIENCE` | `duynhlab-platform` | Expected `aud` |
| `LOG_LEVEL` | `info` | slogx level |
| `PROFILING_ENABLED` | `false` | Pyroscope via `pkg/obsx`; needs `PYROSCOPE_ENDPOINT` |
| `SERVICE_VERSION`, `OTEL_*`, `TRACING_ENABLED` | see `pkg/obsx` | Telemetry export |

[.env.example](./.env.example) lists them. Secrets must not enter Git, command
history, reports or images.

## Behavior

- Staff JWTs are verified by `pkg/authmw` against the staff realm; the
  `backoffice_admin` role is required. 401/403/503 use the shared envelope with
  a stable `code`.
- Freshness is certified per request from the three target-observed heartbeat
  views; a background sampler also measures it every 30 s for the idle-portal
  gauges. Age ≤120 s is healthy, >120 s to ≤300 s is `stale=true`, beyond that
  or unknown is `503 ANALYTICS_UNAVAILABLE`.
- Every query carries server-side limits (2 s, 512 MiB, 2 threads, 1000 rows)
  and a 2 s client context; the overview has a 5 s budget. Exceeded limits fail
  the whole response.
- Shutdown drains HTTP, joins the sampler, closes ClickHouse, logs
  `process.stopped` and flushes telemetry, each step with its own bounded
  context. Startup failure unwinds acquired resources in reverse order.
- `/health` is process liveness; `/ready` performs one serving-view read.

`pkg/httpmw` provides HTTP RED instrumentation and the access log; `pkg/obsx`
owns export; `pkg/logger/slogx` owns context-aware redacted logs. The adapter
emits standard `db.client.operation.duration` and one client span per query
with bounded query summaries; the overview span carries a bounded failure
reason. No SQL, credentials, request filters or business payloads are logged.
