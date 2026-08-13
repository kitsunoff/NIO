---
title: Secrets with sops and age
sidebar_position: 4
description: Give a converge an age key so it can decrypt sops secrets in your flake.
---

# Secrets with sops and age

NIO does not manage, generate or rotate secrets. What it does is **mount an age
private key into the converge pod** so that your flake's own sops tooling can
decrypt what it needs at build time.

## The contract

`NixCluster.spec.ageKeyRef` points at a Secret in the same namespace. The
operator mounts it and sets one environment variable:

| What | Value |
| --- | --- |
| Expected Secret key | `keys.txt` |
| Mount path | `/etc/nio/age` (read-only, mode `0400`) |
| Environment variable | `SOPS_AGE_KEY_FILE=/etc/nio/age/keys.txt` |

That is the entire integration. `sops` and `sops-nix` in your flake pick up
`SOPS_AGE_KEY_FILE` by convention.

:::danger The key name is not validated
`keys.txt` is a hard-coded path in the operator, not a field you can set. If your
Secret stores the key under any other name — `age.agekey`, `key.txt`,
`identity` — the `NixCluster` is accepted without complaint, the pod starts, and
decryption fails at converge time with a sops error that does not mention NIO.

The same applies to `sshKeyRef`, which must use `ssh-privatekey`.
:::

## 1. Create the Secret

If you have an age identity file already:

```sh
kubectl create secret generic cluster-age-key \
  --namespace infra \
  --from-file=keys.txt=$HOME/.config/sops/age/keys.txt
```

Note `--from-file=keys.txt=...` — the part before `=` is the key name inside the
Secret and it must be exactly `keys.txt`. Using `--from-file=$HOME/...` names the
key after the file, which usually works by accident and breaks the moment your
local file is called something else.

To generate a fresh identity:

```sh
nix shell nixpkgs#age --command age-keygen --output keys.txt
```

The public recipient it prints is what goes in your repository's `.sops.yaml`.

## 2. Verify the key name before you rely on it

```sh
kubectl get secret cluster-age-key --namespace infra \
  --output jsonpath='{range $k, $v := .data}{$k}{"\n"}{end}'
```

```text
keys.txt
```

Anything else and it will not work. Recreate the Secret.

## 3. Reference it

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata:
  name: prod
  namespace: infra
spec:
  source:
    gitRepo: https://github.com/example/cluster.git
    ref: main
  sshKeyRef:
    name: cluster-ssh      # Secret key must be: ssh-privatekey
  ageKeyRef:
    name: cluster-age-key  # Secret key must be: keys.txt
  nodeGroups:
    - name: workers
      selector:
        matchLabels:
          role: worker
```

## 4. Confirm the mount reached the pod

```sh
kubectl get nixcronjob prod-converge --namespace infra \
  --output jsonpath='{.spec.cronJobTemplate.jobTemplate.spec.template.spec.volumes[*].name}{"\n"}'
```

You should see `nio-cluster-age` among them, and the environment variable:

```sh
kubectl get nixcronjob prod-converge --namespace infra \
  --output jsonpath='{.spec.cronJobTemplate.jobTemplate.spec.template.spec.containers[*].env[?(@.name=="SOPS_AGE_KEY_FILE")].value}{"\n"}'
```

```text
/etc/nio/age/keys.txt
```

## Secrets for a single host

`NixosConfiguration` has **no** `ageKeyRef`. For a single host, inject the secret
into the source tree with `additionalFiles` using a `SecretRef` source:

```yaml
spec:
  additionalFiles:
    - path: secrets/node-a.key
      valueType: SecretRef
      secretRef:
        name: node-a-secret
        key: private-key
```

Files injected this way are copied into the checkout and force-staged with
`git add --force`, so a git-tree flake sees them even under `.gitignore`.

:::warning Do not use `valueType: Inline` for secrets
Inline content is carried in the pod spec, readable by anyone who can read pods
in the namespace. Use `SecretRef`.
:::

## What NIO deliberately does not do

- It does not generate, rotate or re-encrypt secrets.
- It does not create Kubernetes Secrets from sops files.
- It does not decrypt anything itself — decryption happens inside your flake,
  during the converge, using the key NIO mounted.

Rotation is a matter of updating the Secret and letting the next converge pick it
up.

## Security notes

The namespace holding these Secrets holds the keys to every machine the cluster
manages: one SSH key that reaches every member, and an age key that decrypts
every cluster secret. Restrict `get secret` in it accordingly.

`sshKeyRef` is **cluster-wide** — a single key for all members. There is no
per-member key for `NixCluster`. If per-host key isolation matters to you, use
one `NixosConfiguration` per host, where the key comes from each `Machine`'s own
`sshKeySecretRef`.

## Next

- [Reference: NixCluster](../reference/nixcluster.md).
