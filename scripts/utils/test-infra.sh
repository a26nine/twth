#!/usr/bin/env bash

# Validate and test both Terraform roots without reusing operator backend state.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly PROJECT_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd -P)
readonly BOOTSTRAP_DIR="${PROJECT_ROOT}/infra/bootstrap"
readonly APPLICATION_DIR="${PROJECT_ROOT}/infra/terraform"

test_data_dir=$(mktemp -d)
readonly test_data_dir
trap 'rm -rf "${test_data_dir}"' EXIT

terraform_isolated() {
  local data_dir=$1
  shift
  env \
    -u AWS_ACCESS_KEY_ID \
    -u AWS_PROFILE \
    -u AWS_ROLE_ARN \
    -u AWS_SECRET_ACCESS_KEY \
    -u AWS_SESSION_TOKEN \
    -u AWS_WEB_IDENTITY_TOKEN_FILE \
    TF_DATA_DIR="${data_dir}" \
    terraform "$@"
}

run_root() {
  local root_dir=$1
  local data_dir=$2

  terraform_isolated "${data_dir}" -chdir="${root_dir}" init \
    -backend=false -lockfile=readonly -input=false
  terraform_isolated "${data_dir}" -chdir="${root_dir}" validate
  terraform_isolated "${data_dir}" -chdir="${root_dir}" test
}

cd "${PROJECT_ROOT}"
terraform fmt -check -recursive infra
run_root "${BOOTSTRAP_DIR}" "${test_data_dir}/bootstrap"
run_root "${APPLICATION_DIR}" "${test_data_dir}/application"
