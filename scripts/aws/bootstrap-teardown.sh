#!/usr/bin/env bash

# Destroy bootstrap resources after the application and manual prerequisites are removed.
# Inputs: AWS_PROFILE, CONFIRM_BOOTSTRAP_DESTROY.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

[[ ${CONFIRM_BOOTSTRAP_DESTROY:-} == twth ]] ||
  die "Set CONFIRM_BOOTSTRAP_DESTROY=twth after application teardown, state cleanup, and DNS delegation removal."
require_command terraform

terraform_cli -chdir="${BOOTSTRAP_DIR}" destroy -input=false -auto-approve
