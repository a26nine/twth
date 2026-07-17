#!/usr/bin/env bash

# Verify the production container's externally observable runtime contract.
# Inputs: LOCAL_IMAGE, CONTAINER_NAME, CONTAINER_PORT.

set -Eeuo pipefail

readonly local_image=${LOCAL_IMAGE:-rpc-proxy:local}
readonly container_name=${CONTAINER_NAME:-polygon-rpc-proxy-test}
readonly container_port=${CONTAINER_PORT:-18080}

cleanup() {
  docker rm -f "${container_name}" >/dev/null 2>&1 || true
}

fail() {
  printf 'Error: %s\n' "$1" >&2
  exit 1
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker compose config >/dev/null
cleanup
docker run --detach \
  --name "${container_name}" \
  --publish "127.0.0.1:${container_port}:8080" \
  "${local_image}" >/dev/null

attempts=0
until response=$(curl --fail --silent --show-error \
  "http://127.0.0.1:${container_port}/healthz"); do
  attempts=$((attempts + 1))
  if [[ ${attempts} -ge 30 ]]; then
    docker logs "${container_name}" >&2 || true
    fail "Container health endpoint did not become ready within 30 seconds."
  fi
  sleep 1
done

[[ ${response} == '{"status":"ok"}' ]] ||
  fail "Unexpected /healthz response: ${response}"

configured_user=$(docker inspect --format '{{.Config.User}}' "${container_name}")
[[ ${configured_user} == '65532:65532' ]] ||
  fail "Expected image user 65532:65532, received ${configured_user}."

healthcheck=$(docker inspect \
  --format '{{if .Config.Healthcheck}}present{{else}}absent{{end}}' \
  "${container_name}")
[[ ${healthcheck} == absent ]] || fail "The runtime image must not define a health command."

if docker exec "${container_name}" /bin/sh -c true >/dev/null 2>&1; then
  fail "The scratch runtime unexpectedly contains /bin/sh."
fi

printf 'Container contract checks passed for %s.\n' "${local_image}"
