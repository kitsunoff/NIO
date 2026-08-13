---
title: Converge your first machine
sidebar_label: First machine
sidebar_position: 2
description: Register a host as a Machine and keep it converged with a NixosConfiguration.
---

# Converge your first machine

This walks through the two objects you need to put a real host under NIO's
control: a `Machine` (how to reach it) and a `NixosConfiguration` (what to make
it run).

## Before you start

You need:

- A host reachable over SSH **from the operator's pods**, already running NixOS
  (unless you are doing a full disk install, see below).
- An SSH private key that logs into that host as a user who can run
  `nixos-rebuild switch` — in practice `root`.
- A git repository containing a flake with a NixOS configuration output.

:::danger This changes real machines
A `NixosConfiguration` runs `nixos-rebuild switch` against the host you point it
at, on a schedule, forever. With `fullInstall: true` it runs `nixos-anywhere`,
which **repartitions the target's disks**. Point it at a machine you are willing
to lose.
:::

## 1. The SSH key

The `Machine` reads the private key from a Secret key literally named
`ssh-privatekey`. There is no field to override that name.

```sh
kubectl create secret generic host-ssh-key \
  --namespace default \
  --from-file=ssh-privatekey=$HOME/.ssh/id_ed25519
```

## 2. The Machine

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: node-a
  namespace: default
  labels:
    role: worker
spec:
  host: node-a.example.internal
  sshUser: root
  sshKeySecretRef:
    name: host-ssh-key
```

Apply it, then watch it become discoverable:

```sh
kubectl get machine node-a --watch
```

```text
NAME     HOST                      READY   DISCOVERABLE   CONFIG   AGE
node-a   node-a.example.internal   True    True                    35s
```

The `Machine` controller re-probes SSH reachability **every 60 seconds**, and
collects the host's CPU architecture at most every **12 hours**. That
architecture is the only hardware fact NIO actually stores and the only one
anything reads — see
[the Machine reference](../reference/machine.md#status-hardwarefacts) for what
the rest of the `hardwareFacts` schema does and does not contain.

A `Machine` that never turns `Discoverable` is an SSH problem, not a NIO problem.
Check that the operator's pods can route to `spec.host`, and that the key in the
Secret is the one the host accepts.

## 3. The NixosConfiguration

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixosConfiguration
metadata:
  name: node-a
  namespace: default
spec:
  machineRef:
    name: node-a
  gitRepo: https://github.com/example/nixos-config.git
  ref: main
  flake: "#node-a"
  dayTwoSchedule: "*/30 * * * *"
```

Two things about `spec.flake` that the field name does not tell you:

- **It must include the leading `#`.** The operator builds the installable by
  string-concatenating `"." + spec.flake`, with no separator inserted. `#node-a`
  becomes `.#node-a`. Writing `node-a` produces `.node-a`, which is not a valid
  installable.
- **The `.` is the checkout root**, or `spec.configurationSubdir` under it if you
  set that.

## 4. Watch it converge

```sh
kubectl get nixosconfiguration node-a --watch
```

```text
NAME     READY   PHASE        TARGET   FLAKE     AGE
node-a           Converging   node-a   #node-a   20s
node-a   True    Ready        node-a   #node-a   3m10s
```

`Ready` means the child day-two cron reported a successful run — a real
`nixos-rebuild switch` completed on the real host.

## What actually just happened

`NixosConfiguration` does not talk to your host. It is an **orchestrator**: it
compiles down to child workload objects that do the work.

```sh
kubectl get nixcronjob,nixjob
```

```text
NAME                                       PHASE   SCHEDULE       LASTSCHEDULE   AGE
nixcronjob.nio.homystack.com/node-a-day2   Ready   */30 * * * *   40s            3m
```

You will see:

- `node-a-day2` — a [`NixCronJob`](../reference/nixcronjob.md) running
  `nixos-rebuild switch --flake .#node-a --target-host root@node-a.example.internal`
  on the schedule, with `concurrencyPolicy: Forbid`.
- `node-a-install` — a [`NixJob`](../reference/nixjob.md) running
  `nixos-anywhere`, **only if** you set `fullInstall: true`.
- `node-a-onremove` — a `NixJob` created only on deletion, and only if you set
  `spec.onRemoveFlake`.

Because the day-two child is created with `triggerOnChange: true`, a new commit
on `ref` fires a rebuild promptly rather than waiting for the next cron tick. The
schedule is the self-healing floor, not the reaction time.

## First-time disk install

If the target is bare metal or a fresh VM with no NixOS on it yet, set:

```yaml
spec:
  fullInstall: true
```

The orchestrator then runs a one-shot `nixos-anywhere` child **before** the
day-two cron, and records `status.fullDiskInstallCompleted: true` so it never
runs again. It retries at most **3 times**; after that the configuration sits
`Degraded` and is not retried or recreated until you change the spec.

## Cleaning up

Deleting the `NixosConfiguration` deletes the day-two cron so convergence stops.
It does **not** revert the host by default — the host keeps whatever
configuration it was last given. To hand the host a teardown configuration on
delete, set `spec.onRemoveFlake`; see
[the reference](../reference/nixosconfiguration.md#deletion-and-onremoveflake).

## Next

- [Concepts: machines and configurations](../concepts/machines-and-configurations.md)
  explains the phase machine.
- [Speed up day-two converges](../how-to/accelerate-converge.md) once the naive
  setup gets slow — and it will, because by default every run rebuilds inside a
  pod whose `/nix` dies with it.
