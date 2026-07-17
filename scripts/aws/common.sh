#!/usr/bin/env bash

# Shared paths and validation for the AWS operations scripts.

readonly AWS_SCRIPTS_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly PROJECT_ROOT=$(cd "${AWS_SCRIPTS_DIR}/../.." && pwd -P)
readonly BOOTSTRAP_DIR="${PROJECT_ROOT}/infra/bootstrap"
readonly APPLICATION_DIR="${PROJECT_ROOT}/infra/terraform"

die() {
  printf 'Error: %s\n' "$1" >&2
  return 2
}

require_public_hostname() {
  local value=${1:-}

  [[ -n ${value} ]] || die "PUBLIC_HOSTNAME is required."
  [[ ${value} =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$ ]] ||
    die "PUBLIC_HOSTNAME must be a valid lowercase fully qualified hostname."
}

require_image_digest() {
  local value=${1:-}

  [[ ${value} =~ ^sha256:[0-9a-f]{64}$ ]] ||
    die "IMAGE_DIGEST must be sha256 followed by 64 lowercase hexadecimal characters."
}

require_aws_account_id() {
  local value=${1:-}

  [[ ${value} =~ ^[0-9]{12}$ ]] || die "AWS_ACCOUNT_ID must contain exactly 12 digits."
}

require_command() {
  local command_name=$1

  command -v "${command_name}" >/dev/null 2>&1 || die "Required command not found: ${command_name}"
}

aws_cli() {
  local region=${AWS_REGION:-us-east-1}

  if [[ -n ${AWS_PROFILE:-} ]]; then
    env AWS_PROFILE="${AWS_PROFILE}" aws --region "${region}" "$@"
  else
    env -u AWS_PROFILE aws --region "${region}" "$@"
  fi
}

terraform_cli() {
  if [[ -n ${AWS_PROFILE:-} ]]; then
    env AWS_PROFILE="${AWS_PROFILE}" terraform "$@"
  else
    env -u AWS_PROFILE terraform "$@"
  fi
}

terraform_output_raw() {
  local root_dir=$1
  local output_name=$2

  terraform_cli -chdir="${root_dir}" output -raw "${output_name}"
}

resolve_aws_account_id() {
  terraform_output_raw "${BOOTSTRAP_DIR}" aws_account_id
}

resolve_state_bucket() {
  terraform_output_raw "${BOOTSTRAP_DIR}" state_bucket_name
}
