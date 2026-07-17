#!/usr/bin/env bash

# Initialize the application root against the bootstrapped S3 state bucket.
# Inputs: AWS_PROFILE, TF_STATE_BUCKET.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

require_command terraform

state_bucket=${TF_STATE_BUCKET:-}
if [[ -z ${state_bucket} ]]; then
  state_bucket=$(resolve_state_bucket)
fi
[[ -n ${state_bucket} ]] || die "TF_STATE_BUCKET is required and could not be read from bootstrap output."

terraform_cli -chdir="${APPLICATION_DIR}" init -reconfigure -input=false \
  -backend-config="bucket=${state_bucket}"
