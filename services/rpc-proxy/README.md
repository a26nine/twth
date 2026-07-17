# RPC proxy service

This Go service is an HTTP reverse proxy for JSON-RPC traffic. It forwards request bodies without parsing or re-encoding them, so single requests, batches, notifications, and opaque non-JSON bodies use the same transport path.

For the project overview and AWS deployment, see the [root guide](../../README.md) and [infrastructure guide](../../infra/README.md).

## Quick start

From the repository root, build and start the service with Docker Compose:

```bash
make local-up
curl --fail --silent http://127.0.0.1:8080/healthz
```

The expected response is:

```json
{ "status": "ok" }
```

Stop the Compose service from the repository root:

```bash
make local-down
```

To run the binary without Compose, change to the service directory and set the upstream explicitly:

```bash
cd services/rpc-proxy
UPSTREAM_URL=https://rpc.example.com go run ./cmd/rpc-proxy
```

Print embedded build metadata without starting the server:

```bash
cd services/rpc-proxy
go run ./cmd/rpc-proxy --version
```

## Request routing

Only two requests are handled locally:

| Request        | Response                                              |
| -------------- | ----------------------------------------------------- |
| `GET /healthz` | HTTP 200 with `{"status":"ok"}`.                      |
| `GET /version` | HTTP 200 with the embedded version and source commit. |

Every other method and path is sent to the configured upstream. For example, `POST /healthz` and `HEAD /version` are proxied rather than handled locally.

## Forwarding contract

For proxied requests, the service preserves:

- the HTTP method;
- raw query bytes, including a trailing bare `?`;
- accepted request-body bytes;
- end-to-end request headers; and
- the upstream status, end-to-end headers, content encoding, JSON-RPC errors, and response-body bytes.

The incoming path is joined to any base path in `UPSTREAM_URL`:

```text
UPSTREAM_URL=https://example.com/base
incoming path=/rpc
upstream path=/base/rpc
```

With an upstream that has no base path, the incoming path is unchanged.

The upstream host replaces the incoming `Host`. Go's reverse-proxy transport removes hop-by-hop headers and untrusted forwarding headers; the service then creates trusted `X-Forwarded-For`, `X-Forwarded-Host`, and `X-Forwarded-Proto` values. The standardized `Forwarded` header and protocol-upgrade headers are removed.

Requests are buffered before the upstream is contacted. The service reads no more than `MAX_REQUEST_BYTES + 1`, so an oversized body receives HTTP 413 without starting an upstream request. Accepted bodies are forwarded from memory. Responses are streamed rather than buffered.

## Proxy-generated errors

Transport failures use HTTP JSON errors rather than synthesized JSON-RPC responses:

| Condition                                     | Status | Body                                 |
| --------------------------------------------- | -----: | ------------------------------------ |
| The request body cannot be read               |    400 | `{"error":"invalid request body"}`   |
| The request body exceeds the configured limit |    413 | `{"error":"request body too large"}` |
| The upstream times out                        |    504 | `{"error":"gateway timeout"}`        |
| Another upstream transport error occurs       |    502 | `{"error":"bad gateway"}`            |

Upstream HTTP failures and JSON-RPC error objects pass through unchanged.

## Configuration

| Variable                           | Default                    | Description                                                                                  |
| ---------------------------------- | -------------------------- | -------------------------------------------------------------------------------------------- |
| `LISTEN_ADDR`                      | `:8080`                    | HTTP listener address. Must not be empty.                                                    |
| `UPSTREAM_URL`                     | `https://polygon.drpc.org` | Upstream base URL. Must use HTTP or HTTPS, include a host, and contain no query or fragment. |
| `MAX_REQUEST_BYTES`                | `10485760`                 | Positive maximum request-body size in bytes.                                                 |
| `UPSTREAM_RESPONSE_HEADER_TIMEOUT` | `30s`                      | Positive Go duration allowed for upstream response headers.                                  |
| `SHUTDOWN_TIMEOUT`                 | `15s`                      | Positive Go duration allowed for graceful shutdown.                                          |
| `LOG_LEVEL`                        | `info`                     | Go `slog` level, such as `debug`, `info`, `warn`, or `error`.                                |

Invalid configuration is reported before the listener starts. Credentials in an upstream URL are redacted from logs.

The standalone service accepts HTTP or HTTPS upstreams. The production Terraform configuration requires HTTPS.

## Logging and lifecycle

The service writes structured JSON logs to standard output. A completed request records:

- method;
- path;
- response status;
- response-body bytes written; and
- duration.

Request and response bodies are never logged.

The HTTP server uses these fixed limits:

| Setting              |       Value |
| -------------------- | ----------: |
| Header read timeout  |   5 seconds |
| Request read timeout |  30 seconds |
| Idle timeout         | 120 seconds |
| Maximum header size  |       1 MiB |

On `SIGINT` or `SIGTERM`, the server stops accepting new connections and drains in-flight requests for up to `SHUTDOWN_TIMEOUT`. If the deadline expires, it closes the server and exits with an error.

## Container

The Dockerfile uses a digest-pinned Alpine builder and a `scratch` runtime. The production binary is statically compiled and the runtime image contains only:

- the proxy binary; and
- CA certificates for HTTPS upstreams.

The image runs as `65532:65532` and contains no shell, package manager, or image-level health command. The AWS task definition additionally enables a read-only root filesystem.

From the repository root, build the production `linux/amd64` image:

```bash
make docker-build
```

`LOCAL_IMAGE` overrides the default `rpc-proxy:local` tag. Release builds embed `VERSION` and `COMMIT` in the binary and image metadata.

## Testing

Run these commands from the repository root:

```bash
make test-go
make docker-build
make test-container
```

| Command               | Coverage                                                                      | External effects                                                         |
| --------------------- | ----------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `make test-go`        | Race-enabled Go unit and integration tests                                    | Does not contact a public RPC upstream.                                  |
| `make docker-build`   | Production `linux/amd64` image build                                          | May download the builder image.                                          |
| `make test-container` | Health response, non-root user, missing shell and health command, and cleanup | Builds and runs a temporary local container.                             |
| `make test-live`      | Direct-versus-proxy JSON-RPC compatibility                                    | Calls a public upstream and depends on its availability and rate limits. |
