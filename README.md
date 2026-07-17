# twth

`twth` is a small, production-shaped HTTP reverse proxy for JSON-RPC traffic. The proxy is written in Go and deployed to Amazon ECS on AWS Fargate with Terraform.

The service forwards accepted request bodies without parsing or re-encoding them. Single requests, batch requests, notifications, and opaque non-JSON bodies therefore use the same transport path.

## Choose your path

| Goal                  | Start here                                                                                          |
| --------------------- | --------------------------------------------------------------------------------------------------- |
| Evaluate the project  | Run the [reviewer gate](#reviewer-gate), then read the [design decisions](#design-decisions).       |
| Develop the proxy     | Follow the [quick start](#quick-start), then use the [service guide](services/rpc-proxy/README.md). |
| Deploy or operate AWS | Read the [infrastructure and operations guide](infra/README.md) before running any AWS mutation.    |
| Find a command        | Run `make help` from the repository root.                                                           |

## Architecture

Route 53 resolves the public hostname to an internet-facing Application Load Balancer (ALB). An AWS WAF web ACL is associated with the ALB, which terminates TLS and forwards accepted requests to Fargate tasks:

```text
client
  ├─ DNS lookup ───────────────> Route 53
  └─ HTTPS request ────────────> ALB + AWS WAF
                                    │
                                    v
                               Fargate tasks
                               private subnets
                                    │
                                    v
                               NAT Gateways
                                    │
                                    v
                               HTTPS upstream
```

The ALB and tasks span two availability zones. Tasks have no public IP addresses and accept port `8080` only from the ALB security group. Each private subnet uses a NAT Gateway in the corresponding public subnet for outbound image pulls, logging, and upstream requests.

The release path keeps builds and deployments separate:

```text
version tag
  -> quality checks
  -> multi-platform GHCR image
  -> GitHub artifact attestation
  -> operator-selected version
  -> verified immutable digest
  -> Terraform deployment
  -> ECS stability and smoke checks
```

GitHub Actions uses OpenID Connect (OIDC) and short-lived AWS credentials. The automated deployment verifies image provenance before passing an immutable digest to Terraform; ECS never receives a mutable image tag.

## Design decisions

- **Opaque request forwarding:** The proxy does not interpret JSON-RPC methods or network-specific payloads.
- **Bounded requests, streamed responses:** Requests are buffered so the body limit can be enforced before contacting the upstream. Responses remain streamable.
- **HTTP only:** Protocol upgrades, WebSockets, and subscriptions are excluded to keep the transport contract explicit.
- **Minimal container:** The `scratch` runtime contains only the static binary and CA certificates, runs as `65532:65532`, and has no shell or package manager.
- **Private tasks:** Fargate tasks have no public IP addresses. Two zonally aligned NAT Gateways provide outbound access without exposing task network interfaces directly to the internet.
- **Immutable automated promotion:** Releases are attested, the deployment workflow verifies provenance, and Terraform owns the digest-pinned task definition.
- **Guarded operations:** Account checks, reviewed plans, smoke tests, immutable image checks, and explicit teardown tokens protect production changes.

For exact proxy semantics, see the [service guide](services/rpc-proxy/README.md). For AWS trade-offs and procedures, see the [infrastructure guide](infra/README.md).

## Requirements

The complete local reviewer gate requires:

- Go 1.26
- Terraform 1.7 or later, below 2.0
- Docker with BuildKit/buildx and Docker Compose v2
- GNU Make 3.81 or a compatible implementation
- Bash and `curl`

AWS CLI v2 and `dig` are required only for AWS operations. Account-free checks may still download Go modules, Terraform providers, and container images.

## Quick start

From the repository root, build and start the Compose service:

```bash
make local-up
```

Check the local endpoints:

```bash
curl --fail --silent http://127.0.0.1:8080/healthz
curl --fail --silent http://127.0.0.1:8080/version
```

Expected development responses:

```json
{ "status": "ok" }
```

```json
{ "version": "dev", "commit": "unknown" }
```

Stop the service and remove Compose orphans:

```bash
make local-down
```

The Compose configuration uses `https://polygon.drpc.org` as its development upstream. To select another upstream or run the Go binary directly, follow the [service quick start](services/rpc-proxy/README.md#quick-start).

## Verification

### Reviewer gate

From the repository root, run the complete account-free acceptance suite:

```bash
make verify
```

This command checks formatting, static analysis, race-enabled Go tests, Terraform validation and native tests, shell contracts, the host build, the production image, the Compose model, and the running container's health and security properties.

It may download dependencies and images and creates a temporary local container. It does not call AWS or a public RPC upstream, publish an image, initialize remote Terraform state, apply infrastructure, or destroy resources.

### Faster development check

For a source-only loop that skips the container build and runtime contract:

```bash
make check
```

### Verification boundaries

| Command          | Scope                                                     | External effects                                                         |
| ---------------- | --------------------------------------------------------- | ------------------------------------------------------------------------ |
| `make check`     | Go, Terraform, scripts, and host build                    | May download dependencies; does not call AWS or an RPC upstream.         |
| `make verify`    | `make check` plus production image and container contract | Builds an image and runs a temporary local container.                    |
| `make test-live` | Direct-versus-proxy RPC compatibility                     | Calls a public upstream and depends on its availability and rate limits. |
| `make smoke`     | Deployed endpoint and supporting AWS resources            | Performs read-only HTTPS, DNS, Terraform-state, and AWS checks.          |

Read the [operator guide](infra/README.md) before running `make smoke` or any AWS mutation or teardown command.

## Repository layout

```text
services/rpc-proxy/       Go service, tests, Dockerfile, and service guide
infra/bootstrap/          State, DNS zone, and GitHub OIDC bootstrap root
infra/terraform/          Network, TLS, WAF, ALB, ECS, and autoscaling root
scripts/aws/              Guarded AWS and Terraform operator commands
scripts/tests/            Shell contract tests
scripts/utils/            Local verification and utility entry points
compose.yaml              Local service configuration
Makefile                  Developer, reviewer, and operator command surface
```

## Production scope

This repository demonstrates a production-shaped deployment, not a complete public RPC platform. It intentionally uses one AWS region, one upstream, private tasks with NAT-based egress, per-IP rate limiting, and CPU-based autoscaling.

It does not include client authentication, per-client quotas, caching, upstream failover, WebSockets, distributed tracing, paging alerts, staging, progressive delivery, or automated bootstrap-state backups. Review the [costs and limitations](infra/README.md#costs-and-limitations) before deploying.
