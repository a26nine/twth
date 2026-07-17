#!/usr/bin/env bash

# Verify shared AWS operations validation and command wrapper contracts.
# Inputs: None.

set -Eeuo pipefail

readonly TEST_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly REPOSITORY_ROOT=$(cd "${TEST_DIR}/../.." && pwd -P)

# shellcheck source=../aws/common.sh
source "${REPOSITORY_ROOT}/scripts/aws/common.sh"

tests_run=0

fail_test() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_success() {
  local output

  tests_run=$((tests_run + 1))
  if ! output=$("$@" 2>&1); then
    fail_test "expected success from '$*', got: ${output}"
  fi
}

assert_failure() {
  local expected=$1
  local output
  local status
  shift

  tests_run=$((tests_run + 1))
  set +e
  output=$("$@" 2>&1)
  status=$?
  set -e

  if [[ ${status} -eq 0 ]]; then
    fail_test "expected '$*' to fail"
  fi
  if [[ ${output} != *"${expected}"* ]]; then
    fail_test "expected '$*' error to contain '${expected}', got: ${output}"
  fi
}

valid_digest="sha256:$(printf 'a%.0s' {1..64})"

assert_success require_public_hostname rpc.example.com
assert_success require_public_hostname rpc.dev.example.com
assert_failure "PUBLIC_HOSTNAME is required" require_public_hostname ""
assert_failure "valid lowercase fully qualified hostname" require_public_hostname RPC.EXAMPLE.COM
assert_failure "valid lowercase fully qualified hostname" require_public_hostname -rpc.example.com

assert_success require_image_digest "${valid_digest}"
assert_failure "IMAGE_DIGEST must be sha256" require_image_digest latest
assert_failure "IMAGE_DIGEST must be sha256" require_image_digest "sha256:abc"

assert_success require_aws_account_id 123456789012
assert_failure "AWS_ACCOUNT_ID must contain exactly 12 digits" require_aws_account_id 1234
assert_failure "AWS_ACCOUNT_ID must contain exactly 12 digits" require_aws_account_id 12345678901a

assert_success require_command bash
assert_failure "Required command not found: command-that-does-not-exist" \
  require_command command-that-does-not-exist

stub_dir=$(mktemp -d)
trap 'rm -rf "${stub_dir}"' EXIT
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "profile=%s args=%s\n" "${AWS_PROFILE-unset}" "$*"' \
  >"${stub_dir}/aws"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "profile=%s args=%s\n" "${AWS_PROFILE-unset}" "$*"' \
  >"${stub_dir}/terraform"
chmod +x "${stub_dir}/aws" "${stub_dir}/terraform"

original_path=${PATH}
PATH="${stub_dir}:${PATH}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE=twth-admin AWS_REGION=eu-west-1 aws_cli sts get-caller-identity)
[[ ${output} == "profile=twth-admin args=--region eu-west-1 sts get-caller-identity" ]] ||
  fail_test "aws_cli did not pass profile, region, and arguments: ${output}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE= AWS_REGION=us-east-1 aws_cli sts get-caller-identity)
[[ ${output} == "profile=unset args=--region us-east-1 sts get-caller-identity" ]] ||
  fail_test "aws_cli did not omit an empty profile: ${output}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE=twth-admin terraform_cli -chdir=/tmp/example validate)
[[ ${output} == "profile=twth-admin args=-chdir=/tmp/example validate" ]] ||
  fail_test "terraform_cli did not pass profile and arguments: ${output}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE= terraform_output_raw /tmp/example service_url)
[[ ${output} == "profile=unset args=-chdir=/tmp/example output -raw service_url" ]] ||
  fail_test "terraform_output_raw used an unexpected command: ${output}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE= resolve_aws_account_id)
[[ ${output} == "profile=unset args=-chdir=${BOOTSTRAP_DIR} output -raw aws_account_id" ]] ||
  fail_test "resolve_aws_account_id used an unexpected output: ${output}"

tests_run=$((tests_run + 1))
output=$(AWS_PROFILE= resolve_state_bucket)
[[ ${output} == "profile=unset args=-chdir=${BOOTSTRAP_DIR} output -raw state_bucket_name" ]] ||
  fail_test "resolve_state_bucket used an unexpected output: ${output}"

PATH=${original_path}

printf 'PASS: %d common shell contract tests\n' "${tests_run}"
