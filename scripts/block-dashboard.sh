#!/usr/bin/env bash

# Render a small terminal dashboard for the latest block observed through the RPC proxy.

set -Eeuo pipefail

readonly DEFAULT_POLL_INTERVAL_SECONDS="5"

rpc_proxy_url=${RPC_PROXY_URL:-}
poll_interval_seconds=${POLL_INTERVAL_SECONDS:-${DEFAULT_POLL_INTERVAL_SECONDS}}
run_once=0
last_error="none"

usage() {
  cat <<'USAGE'
Usage: scripts/block-dashboard.sh [--url URL] [--interval SECONDS] [--once]

Poll an EVM-compatible HTTP JSON-RPC endpoint and render the latest block.

Options:
  --url URL             RPC proxy URL. Uses --url, RPC_PROXY_URL, or https://PUBLIC_HOSTNAME.
  --interval SECONDS    Poll interval. Defaults to POLL_INTERVAL_SECONDS or 5.
  --once                Render one refresh and exit.
  --help                Show this help text.
USAGE
}

die() {
  printf 'Error: %s\n' "$1" >&2
  exit 2
}

require_command() {
  local command_name=$1

  command -v "${command_name}" >/dev/null 2>&1 ||
    die "Required command not found: ${command_name}"
}

parse_args() {
  while (($# > 0)); do
    case $1 in
      --url)
        (($# >= 2)) || die "--url requires a value"
        rpc_proxy_url=$2
        shift 2
        ;;
      --interval)
        (($# >= 2)) || die "--interval requires a value"
        poll_interval_seconds=$2
        shift 2
        ;;
      --once)
        run_once=1
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        die "Unknown argument: $1"
        ;;
    esac
  done
}

validate_config() {
  [[ ${poll_interval_seconds} =~ ^[1-9][0-9]*$ ]] ||
    die "POLL_INTERVAL_SECONDS must be a positive integer"

  if [[ -z ${rpc_proxy_url} ]]; then
    [[ -n ${PUBLIC_HOSTNAME:-} ]] ||
      die "PUBLIC_HOSTNAME or RPC_PROXY_URL is required"
    case ${PUBLIC_HOSTNAME} in
      http://*|https://*)
        rpc_proxy_url=${PUBLIC_HOSTNAME}
        ;;
      *)
        rpc_proxy_url="https://${PUBLIC_HOSTNAME}"
        ;;
    esac
  fi
  [[ -n ${rpc_proxy_url} ]] || die "RPC proxy URL must not be empty"
}

hex_to_decimal() {
  local value=${1:-0x0}
  local hex=${value#0x}
  hex=${hex#0X}

  if [[ -z ${hex} ]]; then
    printf '0'
    return
  fi
  printf '%d' "$((16#${hex}))"
}

format_unix_time() {
  local seconds=$1

  if date -u -r "${seconds}" '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
    date -u -r "${seconds}" '+%Y-%m-%dT%H:%M:%SZ'
    return
  fi
  date -u -d "@${seconds}" '+%Y-%m-%dT%H:%M:%SZ'
}

clear_screen() {
  if [[ ${run_once} -eq 0 ]]; then
    if command -v clear >/dev/null 2>&1; then
      clear
    else
      printf '\033[2J\033[H'
    fi
  fi
}

rpc_call() {
  local request_id=$1
  local method=$2
  local params_json=$3
  local body_file
  local payload
  local curl_time
  local curl_output
  local curl_status
  local response_body

  payload=$(jq -cn \
    --argjson id "${request_id}" \
    --arg method "${method}" \
    --argjson params "${params_json}" \
    '{jsonrpc:"2.0",id:$id,method:$method,params:$params}')

  body_file=$(mktemp)
  set +e
  curl_output=$(curl --fail --silent --show-error \
    --connect-timeout 5 \
    --max-time 20 \
    --header 'Content-Type: application/json' \
    --data-binary "${payload}" \
    --output "${body_file}" \
    --write-out '%{time_total}' \
    "${rpc_proxy_url}" 2>&1)
  curl_status=$?
  set -e

  response_body=$(cat "${body_file}")
  rm -f "${body_file}"

  if [[ ${curl_status} -ne 0 ]]; then
    printf 'curl failed: %s\n' "${curl_output}"
    return 1
  fi

  curl_time=${curl_output}
  if ! jq -e . >/dev/null 2>&1 <<<"${response_body}"; then
    printf 'malformed JSON-RPC response\n'
    return 1
  fi
  if jq -e '.error != null' >/dev/null <<<"${response_body}"; then
    jq -r '"JSON-RPC error \(.error.code): \(.error.message)"' <<<"${response_body}"
    return 1
  fi

  jq -cn --argjson body "${response_body}" --arg latency "${curl_time}" \
    '{body:$body, latency:$latency}'
}

fetch_latest_block() {
  local height_rpc
  local block_number
  local block_params
  local block_rpc

  height_rpc=$(rpc_call 1 eth_blockNumber '[]') || {
    printf '%s\n' "${height_rpc}"
    return 1
  }

  block_number=$(jq -er '.body.result | select(type == "string" and test("^0x[0-9a-fA-F]+$"))' \
    <<<"${height_rpc}") || {
    printf 'missing or invalid eth_blockNumber result\n'
    return 1
  }

  block_params=$(jq -cn --arg number "${block_number}" '[$number, false]')
  block_rpc=$(rpc_call 2 eth_getBlockByNumber "${block_params}") || {
    printf '%s\n' "${block_rpc}"
    return 1
  }
  printf '%s\n' "${block_rpc}"
}

render_block() {
  local block_rpc=$1
  local block
  local block_hex
  local block_decimal
  local timestamp_hex
  local timestamp_decimal
  local gas_used_decimal
  local gas_limit_decimal
  local transaction_count
  local latency_ms
  local refreshed_at

  block=$(jq -cer '.body.result | select(type == "object")' <<<"${block_rpc}") || {
    last_error="missing or invalid eth_getBlockByNumber result"
    return 1
  }

  block_hex=$(jq -er '.number' <<<"${block}")
  block_decimal=$(hex_to_decimal "${block_hex}")
  timestamp_hex=$(jq -er '.timestamp' <<<"${block}")
  timestamp_decimal=$(hex_to_decimal "${timestamp_hex}")
  gas_used_decimal=$(hex_to_decimal "$(jq -er '.gasUsed' <<<"${block}")")
  gas_limit_decimal=$(hex_to_decimal "$(jq -er '.gasLimit' <<<"${block}")")
  transaction_count=$(jq -er '.transactions | length' <<<"${block}")
  latency_ms=$(jq -er '(.latency | tonumber * 1000) | round' <<<"${block_rpc}")
  refreshed_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

  clear_screen
  printf 'RPC Proxy Dashboard\n'
  printf 'RPC URL: %s\n' "${rpc_proxy_url}"
  printf 'Poll interval: %s seconds\n' "${poll_interval_seconds}"
  printf 'Last refresh: %s\n' "${refreshed_at}"
  printf 'Last error: %s\n\n' "${last_error}"
  printf 'Block: %s (%s)\n' "${block_decimal}" "${block_hex}"
  printf 'Hash: %s\n' "$(jq -er '.hash' <<<"${block}")"
  printf 'Parent: %s\n' "$(jq -er '.parentHash' <<<"${block}")"
  printf 'Timestamp: %s\n' "$(format_unix_time "${timestamp_decimal}")"
  printf 'Miner: %s\n' "$(jq -er '.miner // "unknown"' <<<"${block}")"
  printf 'Gas: %s / %s\n' "${gas_used_decimal}" "${gas_limit_decimal}"
  printf 'Transactions: %s\n' "${transaction_count}"
  printf 'Latency: %s ms\n' "${latency_ms}"
}

render_error() {
  clear_screen
  printf 'RPC Proxy Dashboard\n'
  printf 'RPC URL: %s\n' "${rpc_proxy_url}"
  printf 'Poll interval: %s seconds\n' "${poll_interval_seconds}"
  printf 'Last refresh: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  printf 'Last error: %s\n' "${last_error}"
}

main() {
  local block_rpc

  parse_args "$@"
  validate_config
  require_command curl
  require_command jq

  trap 'printf "\nStopped block dashboard.\n"; exit 0' INT TERM

  while true; do
    if block_rpc=$(fetch_latest_block); then
      last_error="none"
      render_block "${block_rpc}" || render_error
    else
      last_error=${block_rpc:-unknown dashboard error}
      render_error
    fi

    [[ ${run_once} -eq 0 ]] || break
    sleep "${poll_interval_seconds}"
  done
}

main "$@"
