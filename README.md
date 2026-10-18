# Analytics service

Read-only Go implementation for RFC-0030 commerce analytics. This branch is a
**draft implementation, not deployed or runtime-qualified**. It owns no
transactional data, migrations, gRPC service, or UI.

The [canonical API contract](https://github.com/duynhlab/homelab/blob/feat/rfc0030-phase0/docs/api/analytics.md)
owns routes, payloads, metric semantics and failure behavior. Design and rollout:
[RFC-0030](https://github.com/duynhlab/homelab/tree/main/docs/proposals/rfc/RFC-0030),
[Linux handoff issue](https://github.com/duynhlab/homelab/issues/1266).
The separate Admin UI preview lives in `duynhlab/admin-service`.

## Develop

Use Go 1.27.2. The layers are `internal/web/v1` (transport/auth),
`internal/logic/v1` (freshness and exact arithmetic), and
`internal/core/repository` (bounded native ClickHouse reads).

```bash
go build ./...
go vet ./...
go test -race -count=1 ./...
go test -tags=integration -run '^$' ./...
go -C tools build -o ../.bin/golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint
./.bin/golangci-lint run ./...
```

CI calls the pinned `duynhlab/gha-workflows/go-check.yml` reusable workflow.
Its Test job runs build, vet, race tests with `coverage.out`, and integration
compilation; its Lint job reads the same pinned `tools/go.mod` used locally.
The reusable runtime integration job stays disabled until the Linux gate.

The integration command above **compiles only**; it is not ClickHouse evidence.
Unit tests use in-memory dependencies and an ephemeral local JWKS HTTP server.
They need no containers, real staff credentials or source database.

For a real development run, export the settings illustrated in
[.env.example](./.env.example), provide the restricted ClickHouse read credential,
and run `go run ./cmd`. Validation lives in [config/config.go](./config/config.go).
TLS is verified by default. The explicit insecure switch is for isolated local
stacks only. Secrets must not enter Git, command history, reports or images.
Shutdown drains HTTP before closing ClickHouse and flushing telemetry. Startup
failure unwinds acquired resources in reverse order; cleanup failures return
sanitized errors. If draining exceeds its budget, HTTP connections are force
closed. Handlers must cooperate with cancellation to finish promptly.

The pinned shared `authmw` verifier owns a background JWKS refresher but exposes
neither a shutdown method nor request-context propagation into key retrieval.
The five-second context therefore bounds analytics reads, not a guaranteed
wall-clock limit for authentication during JWKS problems. Track the shared
package fix and Linux outage verification in the handoff issue. `/ready` probes
one serving view; full schema/grant and CDC qualification remains a separate gate.

`pkg/httpmw` provides HTTP RED instrumentation; `pkg/obsx` owns export and
`pkg/logger/slogx` owns context-aware redacted logs. Query duration records only
fixed query names and outcomes; source lag uses the three fixed source names.
No SQL, credentials, request filters or business payloads are logged.

## Linux integration gate

Run against a **disposable ClickHouse 26.9 server**, separate from the platform.
The fixture creates a unique `analytics_test_*` database and restricted test
user, then drops both. It never alters `analytics` or starts a container itself.
The temporary admin connection is test-only; production accepts only
`analytics_reader` and never receives the test credential.

Example preparation on the Linux audit host (not part of the Mac coding gate):

```bash
docker run --rm -d --name analytics-test-ch \
  -p 127.0.0.1:19000:9000 \
  -e CLICKHOUSE_USER=fixture_admin \
  -e CLICKHOUSE_PASSWORD=fixture-only-password \
  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 \
  clickhouse/clickhouse-server:26.9
# Wait until the native server is ready.
export ANALYTICS_TEST_CLICKHOUSE_ADDR=127.0.0.1:19000
export ANALYTICS_TEST_CLICKHOUSE_USER=fixture_admin
export ANALYTICS_TEST_CLICKHOUSE_PASSWORD=fixture-only-password
go test -tags=integration -race -count=1 ./internal/core/repository
docker stop analytics-test-ch
```

The explicit integration test fails if its endpoint is missing. It checks
actual parameter binding and native scans, UTC event/cohort boundaries,
nullable mixed-type order IDs, version deduplication and tombstones, money above
JavaScript's safe range, both ledger legs, reversal/refund signs, product joins,
read-only grants, invalid postings and cancellation. The fixture reproduces the
reviewed view pattern; it is not proof of production grants or CDC behavior.

Before rollout, Linux must additionally verify the platform's actual views and
read profile, TLS and negative grants, heartbeat loss/recovery, staff auth through
Envoy, performance on representative data, telemetry redaction/correlation and
the full local-stack browser audit. No image publication, route exposure, Admin
navigation, deployment or release is authorized by these unit checks.
