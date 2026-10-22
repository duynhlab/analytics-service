#!/usr/bin/env bash
# Called by the shared go-check integration job; also usable on the Linux auditor.
set -euo pipefail
image='clickhouse/clickhouse-server:26.9@sha256:387ebbcca253026def04dc79e83d2323ac7d97bf309ddd090a8a3c702573e01a'
container_id=''
cleanup() {
  status=$?
  if [[ -n "$container_id" ]]; then
    if (( status != 0 )); then docker logs "$container_id" >&2 || true; fi
    docker rm -f "$container_id" >/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT
# Ephemeral fixture credentials; never used by the service or a shared database.
export ANALYTICS_TEST_CLICKHOUSE_USER=analytics_ci
export ANALYTICS_TEST_CLICKHOUSE_PASSWORD=disposable_ci_fixture
container_id=$(docker run -d --rm -p 127.0.0.1::9000 \
  -e CLICKHOUSE_USER="$ANALYTICS_TEST_CLICKHOUSE_USER" \
  -e CLICKHOUSE_PASSWORD="$ANALYTICS_TEST_CLICKHOUSE_PASSWORD" \
  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 "$image")
ready=false
for (( attempt=0; attempt<60; attempt++ )); do
  if docker exec "$container_id" clickhouse-client \
    --user "$ANALYTICS_TEST_CLICKHOUSE_USER" --password "$ANALYTICS_TEST_CLICKHOUSE_PASSWORD" \
    --query 'SELECT 1' >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
if [[ "$ready" != true ]]; then echo 'ClickHouse did not become ready' >&2; exit 1; fi
ANALYTICS_TEST_CLICKHOUSE_ADDR=$(docker port "$container_id" 9000/tcp)
export ANALYTICS_TEST_CLICKHOUSE_ADDR
go test -race -count=1 -tags=integration -covermode=atomic -coverprofile=coverage-integration.out ./...
