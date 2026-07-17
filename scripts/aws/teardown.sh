#!/usr/bin/env bash

# Destroy the application stack after verifying its identity and confirmation token.
# Inputs: AWS_PROFILE, AWS_REGION, AWS_ACCOUNT_ID, PUBLIC_HOSTNAME, CONFIRM_DESTROY.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

[[ ${CONFIRM_DESTROY:-} == twth ]] ||
  die "Set CONFIRM_DESTROY=twth to delete the application."
require_public_hostname "${PUBLIC_HOSTNAME:-}"
for command_name in aws terraform; do
  require_command "${command_name}"
done

account_id=${AWS_ACCOUNT_ID:-}
if [[ -z ${account_id} ]]; then
  account_id=$(resolve_aws_account_id)
fi
require_aws_account_id "${account_id}"

cluster=$(terraform_output_raw "${APPLICATION_DIR}" ecs_cluster_name)
service=$(terraform_output_raw "${APPLICATION_DIR}" ecs_service_name)
task_definition=$(aws_cli ecs describe-services --cluster "${cluster}" --services "${service}" \
  --query 'services[0].taskDefinition' --output text)
deployed_image=$(aws_cli ecs describe-task-definition --task-definition "${task_definition}" \
  --query 'taskDefinition.containerDefinitions[?name==`rpc-proxy`].image | [0]' --output text)

[[ ${deployed_image} =~ ^ghcr\.io/a26nine/twth-rpc-proxy@sha256:[0-9a-f]{64}$ ]] ||
  die "The deployed ECS image is not the expected digest-pinned GHCR image; refusing teardown."
image_digest=${deployed_image##*@}

TF_VAR_aws_account_id="${account_id}" \
TF_VAR_aws_region="${AWS_REGION:-us-east-1}" \
TF_VAR_public_hostname="${PUBLIC_HOSTNAME}" \
TF_VAR_image_digest="${image_digest}" \
  terraform_cli -chdir="${APPLICATION_DIR}" destroy -input=false -auto-approve
