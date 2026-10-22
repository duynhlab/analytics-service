# AGENTS.md

Source of truth for AI agents working in `analytics-service`. Read before any
task.

## Authority and scope

This repository implements the service. It does **not** define the contract.

- **Canonical contract:** [`homelab/docs/api/analytics.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/analytics.md)
- **Shared API rules:** [`homelab/docs/api/api.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/api.md)
- **Telemetry contract:** [`homelab/docs/observability/analytics/`](https://github.com/duynhlab/homelab/blob/main/docs/observability/analytics/README.md)

Implement against those files. When this repository and the contract disagree,
**stop and classify the mismatch** using
[Resolving a mismatch](https://github.com/duynhlab/homelab/blob/main/docs/api/README.md#resolving-a-mismatch)
before changing either side. An implementation that violates the intended
contract **blocks the release tag**.

No route, payload or error-code inventory belongs in this file. Manifests,
gateway routing, NetworkPolicy, ClickHouse schema, CDC and platform
observability belong to [duynhlab/homelab](https://github.com/duynhlab/homelab).

## Contribution workflow for AI agents

- **Never push to `main`.** Branch → PR → squash-merge. Prefixes: `feat/`
  `fix/` `chore/` `docs/` `refactor/` `ci/`.
- Conventional commits; subject ≤50 chars, imperative, no trailing period;
  body wrapped at 72. **No attribution trailers** (no Co-authored-by etc.).
- Verify identity before committing: `git config user.email` must be the
  duynhlab personal identity; `gh` CLI on the `duynhne` account.
- Run the local-stack e2e audit in homelab when the change affects the request
  path or the telemetry it emits.

## Build, test, lint

These are the commands CI runs, so a green local run means a green pipeline.

```bash
go build ./...
go vet ./...
go test -race ./...
bash scripts/test-integration.sh     # needs Docker: disposable ClickHouse 26.9
go -C tools build -o ../.bin/golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint
./.bin/golangci-lint run ./...
```

`tools/go.mod` pins golangci-lint; CI builds the same binary, so local and CI
findings cannot drift.

## Architecture boundaries

**3-layer, dependencies flow one way only: transport → logic → core.**

- **Transport** — `internal/web/v1/` (HTTP, gin). Staff JWT verification and the
  `backoffice_admin` role gate come from `pkg/authmw`; the handler only parses
  filters and maps domain errors to the shared envelope.
- **Logic** — `internal/logic/v1/` owns freshness certification, exact money
  arithmetic (`math/big`) and response assembly.
- **Core** — `internal/core/domain/` (types, sentinel errors, bounded failure
  reasons) and `internal/core/repository/` (the ClickHouse adapter and its SQL).
- **Telemetry** — `internal/telemetry/` runs the idle freshness sampler and the
  observable gauges; it never queries during metric collection.

One binary, one mode: serve. There is no migrate, worker or seed: the service
owns no data.

## Invariants

- **Read only, serving views only.** The ClickHouse identity is
  `analytics_reader` (`config` refuses any other user) and the SQL names only
  `analytics.*_live` views. Raw tables, other databases and any write are
  grant failures by design; a test asserts the negatives.
- **Freshness is certified per request from the three target-observed
  heartbeats.** Age ≤120 s is healthy, >120 s to ≤300 s is `stale=true`, and
  >300 s, missing, duplicate, zero or future is `503 ANALYTICS_UNAVAILABLE`.
  The heartbeats read before the data queries are the ones that certify the
  response; a later heartbeat never certifies earlier data.
- **Money is a canonical decimal integer JSON string**, never a JSON number.
  Counts above JavaScript's safe integer bound make the response unavailable
  rather than lossy.
- **Failures are sanitized.** Public errors are the shared envelope with a
  stable `code`; the bounded `analytics.unavailable.reason` goes on the span.
  No SQL, parameters, driver error text or customer identifiers reach logs,
  spans or metrics.
- **Query budgets are server-side and client-side.** Every query carries
  `max_execution_time 2`, `max_memory_usage 512MiB`, `max_threads 2`,
  `max_result_rows 1000` and a 2 s context; the overview has a 5 s budget.
  Exceeded limits fail the whole response, never a partial aggregate.
- **Shutdown ordering is load-bearing:** HTTP drain → sampler join → pool
  close → `process.stopped` → telemetry flush, each step with its own bounded
  context, so an exhausted drain cannot starve the final export.
- **`CLICKHOUSE_TLS=false` is an explicit deployment decision**, logged at
  start. A CA file without TLS is a configuration error.

## Repository map

- `cmd/main.go`, `cmd/lifecycle.go` — wiring, cleanup stack, graceful shutdown
- `config/config.go` — env config and validation
- `internal/web/v1/` — router, handler, filter parsing
- `internal/logic/v1/` — freshness, assembly, exact arithmetic
- `internal/core/domain/` — types, errors, bounded reasons
- `internal/core/repository/` — ClickHouse adapter, SQL, integration test
- `internal/telemetry/` — freshness sampler and gauges
- `scripts/test-integration.sh` — the disposable ClickHouse gate CI runs

## Gotchas

- Kyverno admission rejects a workload image tagged `:latest` or unpinned. The
  published image is `ghcr.io/duynhlab/analytics-service/analytics-service:<tag>`
  — the repository path repeats, and the tag carries no `v` prefix.
- Metrics leave over OTLP. There is no `/metrics` endpoint and nothing scrapes
  this service.
- The `analytics.*` metric and attribute names are registered in
  `pkg/semconv`; a new name needs the registry change first or the C22 gate
  rejects it.
- `OIDC_STAFF_ISSUER` is an identity claim (public https host);
  `OIDC_STAFF_JWKS_URL` is a network path and may be plain http only for
  `*.svc.cluster.local`, loopback or a bare Compose service name.

## API change synchronization

An API change is not done when the code compiles.

- The contract in homelab and this repository move **together** — same change,
  and either the same PR pair or an immediate follow-up.
- Behaviour that is designed but not deployed is marked **`Planned`** in the
  contract; it is never described as current.
- A material mismatch between the contract and this implementation **blocks the
  release tag** until it is reconciled or explicitly accepted.
