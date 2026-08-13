---
title: Operator flags
sidebar_label: CLI and flags
sidebar_position: 12
description: Command-line flags accepted by the NIO controller manager.
---

# Operator flags

NIO ships a single binary, the controller manager. There is no client CLI — all
interaction is through `kubectl` and the [custom resources](./index.md).

The flags below are the complete set defined by the operator, plus the logging
flags contributed by controller-runtime's zap integration.

## Manager flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--metrics-bind-address` | string | `0` | Address the metrics endpoint binds to. `:8443` for HTTPS, `:8080` for HTTP, `0` to disable the metrics service. |
| `--health-probe-bind-address` | string | `:8081` | Address the health and readiness probe endpoint binds to. |
| `--leader-elect` | bool | `false` | Enable leader election, ensuring only one active controller manager. |
| `--metrics-secure` | bool | `true` | Serve the metrics endpoint over HTTPS. Pass `--metrics-secure=false` for plain HTTP. |
| `--enable-http2` | bool | `false` | Enable HTTP/2 on the metrics and webhook servers. |

:::note Metrics are disabled by default
`--metrics-bind-address` defaults to `0`, which disables the metrics service
entirely. To scrape NIO you must set it explicitly.
:::

:::note HTTP/2 is off by default, deliberately
`--enable-http2` defaults to `false` to avoid the HTTP/2 Stream Cancellation and
Rapid Reset vulnerabilities (GHSA-qppj-fm5r-hxr3, GHSA-4374-p667-p6c8). Turn it
on only if you understand that trade-off.
:::

## Certificate flags

Used when you terminate TLS with your own certificates — for example issued by
cert-manager — rather than the self-signed ones controller-runtime generates.

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--webhook-cert-path` | string | `""` | Directory containing the webhook certificate. Empty disables loading one. |
| `--webhook-cert-name` | string | `tls.crt` | Webhook certificate filename within that directory. |
| `--webhook-cert-key` | string | `tls.key` | Webhook key filename. |
| `--metrics-cert-path` | string | `""` | Directory containing the metrics server certificate. |
| `--metrics-cert-name` | string | `tls.crt` | Metrics certificate filename. |
| `--metrics-cert-key` | string | `tls.key` | Metrics key filename. |

## Logging flags

Contributed by controller-runtime's zap integration:

| Flag | Values | Description |
| --- | --- | --- |
| `--zap-devel` | bool | Development mode: console encoder, debug level, stacktraces at warn. |
| `--zap-encoder` | `json`, `console` | Log encoding. |
| `--zap-log-level` | `debug`, `info`, `error`, or an integer | Verbosity. |
| `--zap-stacktrace-level` | `info`, `error`, `panic` | Level at which stacktraces are captured. |
| `--zap-time-encoding` | `epoch`, `millis`, `nano`, `iso8601`, `rfc3339`, `rfc3339nano` | Timestamp format. |

The operator's defaults are production-oriented: development mode off, and
stacktraces only at `DPanic` and above.

## Defaults in the shipped manifest

The `install.yaml` from a release runs the manager with:

```yaml
args:
  - --leader-elect
  - --health-probe-bind-address=:8081
```

So metrics are **off** in a default install. To enable them, patch the
Deployment's args.

## Health endpoints

Both are served on `--health-probe-bind-address`:

| Path | Purpose |
| --- | --- |
| `/healthz` | Liveness |
| `/readyz` | Readiness |

## Leader election

The lease is identified by `2edf4ad4.homystack.com`. Run more than one replica
only with `--leader-elect`; without it, multiple managers would each reconcile
the same objects and race each other into conflicting `nixos-rebuild` runs on
real hosts.

## Scope

The manager watches **all namespaces**. There is no namespace-restriction flag
and no `WATCH_NAMESPACE` environment variable. To limit scope, limit the
ClusterRole.

*Verified against kitsunoff/NIO@e0dbe5e.*
