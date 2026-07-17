#!/usr/bin/env bash

# Verify AWS operations scripts reject unsafe inputs before external calls.
# Inputs: None.

set -Eeuo pipefail

readonly TEST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly REPOSITORY_ROOT=$(cd "${TEST_DIR}/../.." && pwd -P)

temp_dir=$(mktemp -d)
readonly temp_dir
readonly stub_dir="${temp_dir}/bin"
readonly call_log="${temp_dir}/external-calls.log"
mkdir -p "${stub_dir}"
trap 'rm -rf "${temp_dir}"' EXIT

for command_name in aws curl dig docker terraform; do
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s %s\n" "$(basename "$0")" "$*" >>"${EXTERNAL_CALL_LOG}"' \
    'exit 99' \
    >"${stub_dir}/${command_name}"
  chmod +x "${stub_dir}/${command_name}"
done

tests_run=0

fail_test() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_guarded_failure() {
  local expected=$1
  local script=$2
  local output
  local status
  shift 2

  : >"${call_log}"
  tests_run=$((tests_run + 1))

  set +e
  output=$(env \
    PATH="${stub_dir}:${PATH}" \
    EXTERNAL_CALL_LOG="${call_log}" \
    AWS_PROFILE= \
    AWS_REGION=us-east-1 \
    "$@" \
    bash "${REPOSITORY_ROOT}/${script}" 2>&1)
  status=$?
  set -e

  if [[ ${status} -eq 0 ]]; then
    fail_test "expected ${script} to reject invalid input"
  fi
  if [[ ${output} != *"${expected}"* ]]; then
    fail_test "expected ${script} error to contain '${expected}', got: ${output}"
  fi
  if [[ -s ${call_log} ]]; then
    fail_test "${script} called an external command before validation: $(<"${call_log}")"
  fi
}

valid_digest="sha256:$(printf 'a%.0s' {1..64})"

assert_guarded_failure \
  "PUBLIC_HOSTNAME is required" \
  scripts/aws/plan.sh \
  PUBLIC_HOSTNAME= \
  IMAGE_DIGEST="${valid_digest}"

assert_guarded_failure \
  "IMAGE_DIGEST must be sha256" \
  scripts/aws/plan.sh \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST=latest

assert_guarded_failure \
  "IMAGE_DIGEST must be sha256" \
  scripts/aws/deploy.sh \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST=latest

assert_guarded_failure \
  "Set CONFIRM_DESTROY=twth" \
  scripts/aws/teardown.sh \
  PUBLIC_HOSTNAME=rpc.example.com \
  CONFIRM_DESTROY=no

assert_guarded_failure \
  "Set CONFIRM_BOOTSTRAP_DESTROY=twth" \
  scripts/aws/bootstrap-teardown.sh \
  CONFIRM_BOOTSTRAP_DESTROY=no

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "terraform %s\n" "$*" >>"${EXTERNAL_CALL_LOG}"' \
  'case "$*" in' \
  '  *"output -raw ecs_cluster_name"*) printf "cluster-name\n" ;;' \
  '  *"output -raw ecs_service_name"*) printf "service-name\n" ;;' \
  'esac' \
  >"${stub_dir}/terraform"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "aws %s\n" "$*" >>"${EXTERNAL_CALL_LOG}"' \
  >"${stub_dir}/aws"
chmod +x "${stub_dir}/terraform" "${stub_dir}/aws"

: >"${call_log}"
tests_run=$((tests_run + 1))
env \
  PATH="${stub_dir}:${PATH}" \
  EXTERNAL_CALL_LOG="${call_log}" \
  AWS_PROFILE= \
  AWS_REGION=us-east-1 \
  AWS_ACCOUNT_ID=123456789012 \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST="${valid_digest}" \
  bash "${REPOSITORY_ROOT}/scripts/aws/deploy.sh"

calls=()
while IFS= read -r call; do
  calls[${#calls[@]}]=${call}
done <"${call_log}"

[[ ${#calls[@]} -eq 4 ]] ||
  fail_test "expected deploy to make 4 external calls, received ${#calls[@]}: $(<"${call_log}")"
[[ ${calls[0]} == *'apply -input=false -auto-approve' ]] ||
  fail_test "deploy did not apply before waiting: ${calls[0]}"
[[ ${calls[1]} == *'output -raw ecs_cluster_name' ]] ||
  fail_test "deploy did not resolve the ECS cluster: ${calls[1]}"
[[ ${calls[2]} == *'output -raw ecs_service_name' ]] ||
  fail_test "deploy did not resolve the ECS service: ${calls[2]}"
[[ ${calls[3]} == 'aws --region us-east-1 ecs wait services-stable --cluster cluster-name --services service-name' ]] ||
  fail_test "deploy used an unexpected ECS wait command: ${calls[3]}"

[[ ! -e ${REPOSITORY_ROOT}/scripts/aws/wait.sh ]] ||
  fail_test "ECS waiting must be owned by deploy.sh, not scripts/aws/wait.sh"

printf 'PASS: %d operation guard tests\n' "${tests_run}"
