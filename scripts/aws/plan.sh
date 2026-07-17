#!/usr/bin/env bash

# Plan the application deployment for one immutable GHCR image digest.
# Inputs: AWS_PROFILE, AWS_REGION, AWS_ACCOUNT_ID, PUBLIC_HOSTNAME, IMAGE_DIGEST.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

require_public_hostname "${PUBLIC_HOSTNAME:-}"
require_image_digest "${IMAGE_DIGEST:-}"
require_command terraform

account_id=${AWS_ACCOUNT_ID:-}
if [[ -z ${account_id} ]]; then
  account_id=$(resolve_aws_account_id)
fi
require_aws_account_id "${account_id}"

TF_VAR_aws_account_id="${account_id}" \
TF_VAR_aws_region="${AWS_REGION:-us-east-1}" \
TF_VAR_public_hostname="${PUBLIC_HOSTNAME}" \
TF_VAR_image_digest="${IMAGE_DIGEST}" \
  terraform_cli -chdir="${APPLICATION_DIR}" plan -input=false
