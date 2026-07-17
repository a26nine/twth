#!/usr/bin/env bash

# Verify the live HTTPS endpoint and its supporting AWS resources.
# Inputs: AWS_PROFILE, AWS_REGION, PUBLIC_HOSTNAME, optional IMAGE_DIGEST.

set -Eeuo pipefail

readonly SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

require_public_hostname "${PUBLIC_HOSTNAME:-}"
if [[ -n ${IMAGE_DIGEST:-} ]]; then
  require_image_digest "${IMAGE_DIGEST}"
fi
for command_name in aws curl dig grep sed sort terraform tr; do
  require_command "${command_name}"
done

service_url=$(terraform_output_raw "${APPLICATION_DIR}" service_url)
redirect_status=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  "http://${PUBLIC_HOSTNAME}/healthz")
[[ ${redirect_status} == 301 ]] || die "Expected HTTP 301 redirect, received ${redirect_status}."

headers=$(curl --silent --show-error --dump-header - --output /dev/null \
  "${service_url}/healthz")
grep -Eqi '^strict-transport-security: max-age=63072000; includeSubDomains; preload' <<<"${headers}" ||
  die "The HTTPS response is missing the required Strict-Transport-Security header."
if grep -Eqi '^server:' <<<"${headers}"; then
  die "The ALB server response header is unexpectedly visible."
fi

curl --fail --silent --show-error "${service_url}/healthz" >/dev/null
chain_id=$(curl --fail --silent --show-error \
  --header 'content-type: application/json' \
  --data '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}' \
  "${service_url}/")
grep -Eq '"result"[[:space:]]*:[[:space:]]*"0x89"' <<<"${chain_id}" ||
  die "The live RPC response did not contain Polygon chain ID 0x89."

cluster=$(terraform_output_raw "${APPLICATION_DIR}" ecs_cluster_name)
service=$(terraform_output_raw "${APPLICATION_DIR}" ecs_service_name)
running=$(aws_cli ecs describe-services --cluster "${cluster}" --services "${service}" \
  --query 'services[0].runningCount' --output text)
[[ ${running} =~ ^[0-9]+$ ]] || die "ECS returned an invalid running task count: ${running}."
((running >= 2 && running <= 4)) ||
  die "Expected 2-4 running ECS tasks, received ${running}."

target_group=$(terraform_output_raw "${APPLICATION_DIR}" target_group_arn)
healthy=$(aws_cli elbv2 describe-target-health --target-group-arn "${target_group}" \
  --query 'length(TargetHealthDescriptions[?TargetHealth.State==`healthy`])' --output text)
[[ ${healthy} == "${running}" ]] ||
  die "Expected ${running} healthy targets, received ${healthy}."

[[ -n $(dig +short A "${PUBLIC_HOSTNAME}") ]] ||
  die "${PUBLIC_HOSTNAME} does not resolve to an A record."

zone_id=$(terraform_output_raw "${APPLICATION_DIR}" route53_zone_id)
expected_name_servers=$(aws_cli route53 get-hosted-zone --id "${zone_id}" \
  --query 'DelegationSet.NameServers' --output text | tr '\t' '\n' | sed 's/\.$//' | sort)
actual_name_servers=$(dig +short NS "${PUBLIC_HOSTNAME}" | sed 's/\.$//' | sort)
[[ ${actual_name_servers} == "${expected_name_servers}" ]] ||
  die "Public DNS delegation does not match the Route 53 hosted zone."

certificate_arn=$(terraform_output_raw "${APPLICATION_DIR}" certificate_arn)
certificate_status=$(aws_cli acm describe-certificate --certificate-arn "${certificate_arn}" \
  --query 'Certificate.Status' --output text)
certificate_name=$(aws_cli acm describe-certificate --certificate-arn "${certificate_arn}" \
  --query 'Certificate.DomainName' --output text)
[[ ${certificate_status} == ISSUED && ${certificate_name} == "${PUBLIC_HOSTNAME}" ]] ||
  die "The ACM certificate is not issued for ${PUBLIC_HOSTNAME}."

alb_arn=$(terraform_output_raw "${APPLICATION_DIR}" alb_arn)
aws_cli wafv2 get-web-acl-for-resource --resource-arn "${alb_arn}" >/dev/null

if [[ -n ${IMAGE_DIGEST:-} ]]; then
  task_definition=$(aws_cli ecs describe-services --cluster "${cluster}" --services "${service}" \
    --query 'services[0].taskDefinition' --output text)
  running_image=$(aws_cli ecs describe-task-definition --task-definition "${task_definition}" \
    --query 'taskDefinition.containerDefinitions[?name==`rpc-proxy`].image | [0]' --output text)
  [[ ${running_image} == "ghcr.io/a26nine/twth-rpc-proxy@${IMAGE_DIGEST}" ]] ||
    die "The running ECS image does not match IMAGE_DIGEST."
fi

log_group="/ecs/$(terraform_output_raw "${APPLICATION_DIR}" ecs_service_name)"
aws_cli logs describe-log-streams --log-group-name "${log_group}" --limit 1 >/dev/null

printf 'Production smoke checks passed for %s.\n' "${service_url}"
