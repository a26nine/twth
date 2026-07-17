# Infrastructure and operations

This guide is the operator runbook for bootstrapping, deploying, verifying, rolling back, troubleshooting, and removing the AWS environment.

For project evaluation and local proxy development, use the [root guide](../README.md) and [service guide](../services/rpc-proxy/README.md).

## Architecture

Route 53 resolves the service hostname to an internet-facing Application Load Balancer (ALB). An associated AWS WAF web ACL rate-limits requests before the ALB forwards accepted traffic to Fargate tasks:

```text
internet
  │
  ├─ Route 53 hosted zone
  │
  v
ALB + AWS WAF
public subnets in two availability zones
  │
  v
Fargate tasks
private subnets in two availability zones
  │
  v
NAT Gateway per availability zone
  │
  v
GHCR, CloudWatch Logs, and HTTPS JSON-RPC upstream
```

The network uses two public and two private subnets:

- The ALB and two NAT Gateways run in public subnets.
- Each NAT Gateway has an Elastic IP address and provides egress for the private subnet in the same availability zone.
- Fargate tasks run in private subnets with `assign_public_ip = false`.
- The task security group accepts port `8080` only from the ALB security group.
- Tasks can establish outbound HTTPS connections on port `443` through the NAT Gateways.

The service starts with two tasks and scales between two and four tasks at a 70% average CPU target by default. ECS uses a task execution role for CloudWatch log delivery, but the task definition has no application task role, so the application receives no AWS API credentials.

Terraform is split into two roots:

| Root              | State                   | Responsibility                                                                                   |
| ----------------- | ----------------------- | ------------------------------------------------------------------------------------------------ |
| `infra/bootstrap` | Local                   | S3 application-state bucket, delegated Route 53 zone, GitHub OIDC provider, and deployment role. |
| `infra/terraform` | Bootstrapped S3 backend | VPC, subnets, NAT Gateways, TLS, AWS WAF, ALB, ECS service, autoscaling, and logs.               |

## Before you deploy

> **Cost warning:** Bootstrap and application deployment create billable AWS resources. The default application includes two NAT Gateways, two Elastic IPv4 addresses, an ALB, two Fargate tasks, AWS WAF, CloudWatch Logs, Route 53, and S3 storage. Review [costs and limitations](#costs-and-limitations) before applying Terraform.

AWS operations require:

- Terraform 1.7 or later, below 2.0
- AWS CLI v2 with an authenticated profile and the required permissions
- GitHub CLI for OIDC configuration and local provenance verification
- `dig` for DNS checks
- a public hostname that can be delegated to Route 53
- a released and attested image digest for application deployment

All commands in this guide run from the repository root. Examples use:

```text
AWS_PROFILE=twth-admin
AWS_REGION=us-east-1
PUBLIC_HOSTNAME=rpc.example.com
```

Run `make help` for the authoritative command and variable list.

## Bootstrap the AWS account

Bootstrap creates billable resources and intentionally keeps local Terraform state. Preserve and back up `infra/bootstrap/terraform.tfstate`; the state cannot live in the bucket that it creates.

### 1. Configure bootstrap variables

Create the untracked variable file:

```bash
cp infra/bootstrap/terraform.tfvars.example infra/bootstrap/terraform.tfvars
```

Set the delegated hostname and the GitHub repository identity. Use the repository owner's numeric ID and the repository's numeric ID, not only their mutable names:

```hcl
public_hostname      = "rpc.example.com"
github_owner         = "example-org"
github_owner_id      = 11111111
github_repository    = "example-repository"
github_repository_id = 22222222
```

### 2. Initialize and review

Initialize the local-state root and inspect the proposed resources:

```bash
make bootstrap-init AWS_PROFILE=twth-admin
make bootstrap-plan AWS_PROFILE=twth-admin
```

Confirm the AWS account, region, hostname, state bucket, hosted zone, OIDC provider, and deployment role before continuing.

### 3. Apply bootstrap

> `make bootstrap` recomputes the plan and uses Terraform's automatic approval. Run it immediately after reviewing the plan and stop if the configuration or AWS account has changed.

```bash
make bootstrap AWS_PROFILE=twth-admin
make bootstrap-outputs
```

Preserve the outputs with the local state. You will use them for DNS delegation, GitHub repository variables, and application backend initialization.

### 4. Delegate the hostname

Add the `route53_name_servers` output as NS records at the parent DNS provider. Wait until public DNS returns the same delegation:

```bash
dig +short NS rpc.example.com
```

### 5. Initialize application state

Initialize the application root against the S3 backend:

```bash
make backend-init AWS_PROFILE=twth-admin
```

The command reads `state_bucket_name` from bootstrap output. Set `TF_STATE_BUCKET` explicitly if the bootstrap state is not available locally.

## Configure GitHub deployment

The bootstrap trust policy restricts deployment to the configured repository IDs and the `main` branch. It expects GitHub's immutable, ID-backed OIDC subject format.

The following commands target the canonical `a26nine/twth` repository. Replace that path when bootstrapping another repository.

Check the current subject customization:

```bash
gh api repos/a26nine/twth/actions/oidc/customization/sub
```

If `use_immutable_subject` is `false`, a repository administrator can enable it:

```bash
gh api \
  --method PUT \
  repos/a26nine/twth/actions/oidc/customization/sub \
  -F use_default=true \
  -F use_immutable_subject=true
```

Create these GitHub Actions repository variables:

| Variable          | Value                                     |
| ----------------- | ----------------------------------------- |
| `AWS_ACCOUNT_ID`  | Bootstrap `aws_account_id` output         |
| `AWS_ROLE_ARN`    | Bootstrap `github_deploy_role_arn` output |
| `TF_STATE_BUCKET` | Bootstrap `state_bucket_name` output      |
| `PUBLIC_HOSTNAME` | Hostname delegated during bootstrap       |

No long-lived AWS access key is required. GitHub exchanges its OIDC token for a short-lived role session.

## Release and provenance

Release tags use `rpc-proxy/v<semver>`, for example `rpc-proxy/v0.1.0`.

The `RPC Proxy Release` workflow:

1. validates the version and runs the Go quality gate;
2. publishes `linux/amd64` and `linux/arm64` images to GHCR;
3. records OCI source metadata;
4. creates a GitHub artifact attestation for the image digest; and
5. creates the corresponding GitHub release.

The `RPC Proxy Deployment` workflow accepts a version, resolves it to an immutable manifest digest, confirms that a `linux/amd64` image exists, and verifies the repository, signer workflow, and source tag through the attestation. Only the verified digest is passed to Terraform.

## Deploy

### Automated production deployment

1. Merge the release commit to `main`.
2. Push a tag such as `rpc-proxy/v0.1.0`.
3. Wait for **RPC Proxy Release** to publish and attest the image.
4. Open **Actions → RPC Proxy Deployment** from `main`.
5. Run the workflow with `0.1.0` as `image_version`, without the tag prefix.
6. Review the deployment and smoke-test logs.

The workflow serializes production changes and does not cancel an active deployment. It authenticates to AWS through OIDC, validates the expected account, verifies image provenance, applies Terraform, waits for ECS stability, and runs production smoke checks.

### Local recovery deployment

Use this path only when the automated workflow is unavailable.

> **Provenance boundary:** The local scripts validate the digest format and deploy the repository's digest-pinned image URI, but they do not verify its GitHub artifact attestation. Verify provenance independently before continuing.

Given a released version and its manifest digest, verify the image:

```bash
gh attestation verify \
  "oci://ghcr.io/a26nine/twth-rpc-proxy@sha256:<64-lowercase-hex-characters>" \
  --repo a26nine/twth \
  --signer-workflow a26nine/twth/.github/workflows/rpc-proxy-release.yml \
  --source-ref refs/tags/rpc-proxy/v0.1.0
```

Initialize the backend, review the deployment, and then apply the same inputs:

```bash
make backend-init AWS_PROFILE=twth-admin

make plan \
  AWS_PROFILE=twth-admin \
  AWS_ACCOUNT_ID=123456789012 \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST=sha256:<64-lowercase-hex-characters>

make deploy \
  AWS_PROFILE=twth-admin \
  AWS_ACCOUNT_ID=123456789012 \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST=sha256:<64-lowercase-hex-characters>
```

> `make deploy` recomputes the Terraform plan, uses automatic approval, and waits for ECS stability. Run it immediately after reviewing `make plan` and stop if any input or infrastructure state has changed.

## Verify and roll back

### Verify production

Run production checks after deployment:

```bash
make smoke \
  AWS_PROFILE=twth-admin \
  PUBLIC_HOSTNAME=rpc.example.com \
  IMAGE_DIGEST=sha256:<64-lowercase-hex-characters>
```

`make smoke` performs read-only HTTPS, DNS, Terraform-state, and AWS API checks. It verifies:

- HTTP-to-HTTPS redirect and HSTS;
- health and the live Polygon JSON-RPC contract;
- ECS running count and ALB target health;
- DNS delegation and ACM certificate state;
- AWS WAF association;
- the running task-definition digest when `IMAGE_DIGEST` is set; and
- CloudWatch log access.

`IMAGE_DIGEST` is optional, but include it after deployment to confirm that the intended release is running. The live RPC check calls the configured public upstream and depends on its availability.

### Roll back

Rollback is a forward deployment of an earlier known-good release:

1. Open **Actions → RPC Proxy Deployment** from `main`.
2. Enter the earlier version.
3. Run the workflow.
4. Confirm the resulting smoke checks.

Do not edit ECS task definitions manually or deploy mutable image tags.

## Troubleshooting

| Symptom                                       | Check                                                                                                  |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Required repository-variable validation fails | Set `AWS_ACCOUNT_ID`, `AWS_ROLE_ARN`, `TF_STATE_BUCKET`, and `PUBLIC_HOSTNAME` from bootstrap outputs. |
| Account validation fails                      | Verify `AWS_PROFILE`, `AWS_ACCOUNT_ID`, and `aws sts get-caller-identity`.                             |
| The backend bucket cannot be resolved         | Restore the bootstrap local state or pass `TF_STATE_BUCKET` explicitly.                                |
| ACM validation remains pending                | Confirm that public NS delegation matches `route53_name_servers`.                                      |
| Fargate cannot pull the image or write logs   | Check private routes, NAT Gateway state, HTTPS egress, and the execution role.                         |
| ECS does not stabilize                        | Inspect service events, target health, stopped-task reasons, routing, and CloudWatch logs.             |
| Health succeeds but RPC calls fail            | Check NAT egress, the configured upstream, and proxy transport logs.                                   |
| Smoke reports the wrong image                 | Confirm the selected digest and the completed Terraform apply.                                         |
| Terraform reports a state lock                | Confirm that no deployment is active before investigating the S3 `.tflock`; never remove a valid lock. |
| Teardown is refused                           | Supply the exact confirmation token and confirm ECS uses the expected digest-pinned repository.        |

Useful outputs include `service_url`, `alb_dns_name`, `ecs_cluster_name`, and `ecs_service_name`:

```bash
terraform -chdir=infra/terraform output service_url
```

## Tear down

> **Destructive operation:** Teardown permanently removes application resources. Destroy the application root before bootstrap, preserve required state and audit data, and verify the AWS account and hostname.

### 1. Destroy the application

```bash
make teardown \
  AWS_PROFILE=twth-admin \
  AWS_ACCOUNT_ID=123456789012 \
  PUBLIC_HOSTNAME=rpc.example.com \
  CONFIRM_DESTROY=twth
```

The script verifies the account inputs and refuses to continue unless the running ECS task definition uses the expected digest-pinned GHCR repository. Terraform then destroys the application with automatic approval.

### 2. Prepare bootstrap for deletion

Before destroying bootstrap:

- confirm that the application stack is gone;
- preserve state and outputs required for audit or recovery;
- empty all object versions from the state bucket if the bucket must be deleted;
- remove nonessential records from the hosted zone; and
- remove the parent DNS delegation after retiring the service.

### 3. Destroy bootstrap

```bash
make bootstrap-teardown \
  AWS_PROFILE=twth-admin \
  CONFIRM_BOOTSTRAP_DESTROY=twth
```

The command uses automatic approval after checking the explicit confirmation token. Retain the bootstrap local state until AWS resource deletion is confirmed.

## Costs and limitations

The default application creates resources with fixed and usage-based charges:

- two NAT Gateways, including hourly and per-gigabyte data-processing charges;
- two Elastic IPv4 addresses used by the NAT Gateways;
- one Application Load Balancer;
- two to four Fargate tasks;
- one regional AWS WAF web ACL;
- CloudWatch Logs ingestion and storage;
- S3 application-state versions;
- a Route 53 hosted zone and DNS queries; and
- internet and inter-service data transfer where applicable.

Pricing varies by region and changes over time. Check current AWS pricing for the selected region and destroy unused environments.
