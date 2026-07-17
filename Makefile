SHELL := /bin/sh
.DEFAULT_GOAL := help

SERVICE_DIR := services/rpc-proxy
BOOTSTRAP_DIR := infra/bootstrap
APPLICATION_DIR := infra/terraform

LOCAL_IMAGE ?= rpc-proxy:local
CONTAINER_NAME ?= polygon-rpc-proxy-test
CONTAINER_PORT ?= 18080

AWS_PROFILE ?= twth-admin
AWS_REGION ?= us-east-1
AWS_ACCOUNT_ID ?=
TF_STATE_BUCKET ?=
IMAGE_DIGEST ?=
PUBLIC_HOSTNAME ?=
CONFIRM_DESTROY ?=
CONFIRM_BOOTSTRAP_DESTROY ?=

export AWS_PROFILE AWS_REGION AWS_ACCOUNT_ID TF_STATE_BUCKET
export IMAGE_DIGEST PUBLIC_HOSTNAME CONFIRM_DESTROY CONFIRM_BOOTSTRAP_DESTROY
export LOCAL_IMAGE CONTAINER_NAME CONTAINER_PORT

.PHONY: help fmt fmt-check vet test-go test-infra test build \
	docker-build scripts-check test-container check verify \
	local-up local-down dashboard test-live \
	bootstrap-init bootstrap-plan bootstrap bootstrap-outputs \
	backend-init plan deploy smoke teardown bootstrap-teardown

help: ## Show targets and operator inputs
	@printf 'Usage: make <target> [VARIABLE=value]\n'
	@awk 'BEGIN { FS = ":.*## " } \
		/^##@ / { printf "\n%s\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_-]+:.*## / { printf "  %-20s %s\n", $$1, $$2 }' \
		$(MAKEFILE_LIST)
	@printf '\nOperator inputs:\n'
	@printf '  %-26s %s\n' 'AWS_PROFILE' 'Local AWS profile (default: twth-admin; empty in OIDC CI)'
	@printf '  %-26s %s\n' 'AWS_REGION' 'Deployment region (default: us-east-1)'
	@printf '  %-26s %s\n' 'AWS_ACCOUNT_ID' 'Expected 12-digit AWS account ID'
	@printf '  %-26s %s\n' 'TF_STATE_BUCKET' 'Application state bucket; resolved from bootstrap when empty'
	@printf '  %-26s %s\n' 'PUBLIC_HOSTNAME' 'Public service hostname'
	@printf '  %-26s %s\n' 'IMAGE_DIGEST' 'Immutable sha256 GHCR image digest'
	@printf '  %-26s %s\n' 'CONFIRM_DESTROY' 'Set to twth for application teardown'
	@printf '  %-26s %s\n' 'CONFIRM_BOOTSTRAP_DESTROY' 'Set to twth for bootstrap teardown'

##@ Development and verification

fmt: ## Format Go and Terraform sources
	cd $(SERVICE_DIR) && gofmt -w .
	terraform fmt -recursive infra

fmt-check: ## Check Go and Terraform source formatting
	cd $(SERVICE_DIR) && test -z "$$(gofmt -l .)"
	terraform fmt -check -recursive infra

vet: ## Run Go static analysis
	cd $(SERVICE_DIR) && go vet ./...

test-go: ## Run Go tests with the race detector
	cd $(SERVICE_DIR) && go test -race ./...

test-infra: ## Validate and test both Terraform roots
	bash scripts/utils/test-infra.sh

test: test-go test-infra ## Run Go and Terraform tests

build: ## Build the Go service for the host
	cd $(SERVICE_DIR) && go build ./...

docker-build: ## Build and load the linux/amd64 production image
	docker buildx build --load --platform linux/amd64 \
		--tag $(LOCAL_IMAGE) $(SERVICE_DIR)

scripts-check: ## Parse and test repository operations scripts
	bash -n scripts/aws/*.sh scripts/tests/*.sh scripts/utils/*.sh
	bash scripts/tests/common_test.sh
	bash scripts/tests/operation_guards_test.sh
	bash scripts/tests/infra_isolation_test.sh

test-container: docker-build ## Verify the production container contract
	bash scripts/utils/test-container.sh

check: fmt-check vet test build scripts-check ## Run deterministic source and unit checks

verify: check test-container ## Run the complete account-free acceptance gate

##@ Local runtime

local-up: ## Build and start the local Compose service
	docker compose up --build --detach

local-down: ## Stop the local Compose service and remove orphans
	docker compose down --remove-orphans

dashboard: ## Show latest block data through the deployed RPC proxy
	bash scripts/utils/block-dashboard.sh

test-live: ## Run the optional live dRPC compatibility test
	cd $(SERVICE_DIR) && RPC_LIVE=1 go test -race -count=1 -v \
		-run '^TestLiveRPCCompatibility$$' ./internal/proxy

##@ AWS bootstrap

bootstrap-init: ## Initialize the local-state bootstrap root
	$(if $(strip $(AWS_PROFILE)),AWS_PROFILE="$(AWS_PROFILE)") \
		terraform -chdir=$(BOOTSTRAP_DIR) init -input=false

bootstrap-plan: bootstrap-init ## Plan account bootstrap resources
	$(if $(strip $(AWS_PROFILE)),AWS_PROFILE="$(AWS_PROFILE)") \
		terraform -chdir=$(BOOTSTRAP_DIR) plan -input=false

bootstrap: bootstrap-init ## Create state, DNS, and GitHub OIDC resources
	$(if $(strip $(AWS_PROFILE)),AWS_PROFILE="$(AWS_PROFILE)") \
		terraform -chdir=$(BOOTSTRAP_DIR) apply -input=false -auto-approve

bootstrap-outputs: ## Show account, state, DNS, and OIDC outputs
	terraform -chdir=$(BOOTSTRAP_DIR) output

##@ AWS deployment

backend-init: ## Initialize the application S3 backend
	bash scripts/aws/backend-init.sh

plan: ## Plan the digest-pinned application deployment
	bash scripts/aws/plan.sh

deploy: ## Apply the digest-pinned deployment and wait for ECS
	bash scripts/aws/deploy.sh

smoke: ## Run live HTTPS and AWS production checks
	bash scripts/aws/smoke.sh

##@ Destructive operations

teardown: ## Destroy the application with CONFIRM_DESTROY=twth
	bash scripts/aws/teardown.sh

bootstrap-teardown: ## Destroy bootstrap with CONFIRM_BOOTSTRAP_DESTROY=twth
	bash scripts/aws/bootstrap-teardown.sh
