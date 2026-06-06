# spiffile-operator

[![test](https://github.com/PeterSR/spiffile-operator/actions/workflows/test.yml/badge.svg)](https://github.com/PeterSR/spiffile-operator/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/PeterSR/spiffile-operator)](https://goreportcard.com/report/github.com/PeterSR/spiffile-operator)
[![Release](https://img.shields.io/github/v/release/PeterSR/spiffile-operator)](https://github.com/PeterSR/spiffile-operator/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**A Kubernetes operator for the [spiffile](https://github.com/PeterSR/spiffile)
profile: declare a `ServiceIdentity`, get SPIFFE identity files.**

[spiffile](https://github.com/PeterSR/spiffile) delivers SPIFFE identities as
plain files — no agents, no gRPC socket. This operator is a *producer* for
that profile on Kubernetes: it turns a small custom resource into the
keypair Secret and trust bundle ConfigMap your workloads mount.

```yaml
apiVersion: spiffile.io/v1alpha1
kind: ServiceIdentity
metadata:
  name: billing
  namespace: shop
spec:
  trustDomain: example.org
```

The operator then maintains:

- **Secret `billing-spiffile`** (in `shop`): the identity's private key
  (`key.pem`) and SPIFFE ID (`id`) — `spiffe://example.org/billing`
- **ConfigMap `spiffile-bundle`** (in every namespace that has identities):
  the trust bundle, one data key per trust domain (`example.org.json`),
  binding each identity to its public keys

Workloads consume them with any spiffile library:

```yaml
spec:
  containers:
    - name: app
      env:
        - name: SPIFFILE_ID_FILE
          value: /identity/id
        - name: SPIFFILE_KEY_FILE
          value: /identity/key.pem
        - name: SPIFFILE_BUNDLE_FILE
          value: /bundle/example.org.json  # one data key per trust domain
      volumeMounts:
        - { name: identity, mountPath: /identity, readOnly: true }
        - { name: bundle, mountPath: /bundle, readOnly: true }
  volumes:
    - name: identity
      secret: { secretName: billing-spiffile }
    - name: bundle
      configMap: { name: spiffile-bundle }
```

That's it — the service can now mint audience-bound JWT-SVIDs and verify its
callers, fully offline.

> Mount the directories whole, as above — never with `subPath`. Kubernetes
> does not propagate updates into `subPath` mounts, which would silently
> break key rotation.

## Status

**Alpha.** The `v1alpha1` API may change in breaking ways before v1.0 —
[CHANGELOG.md](CHANGELOG.md) will call them out. Built and tested against the
Kubernetes 1.32 client libraries; recent clusters are expected to work.

## Install

Helm, from the released OCI chart:

```bash
helm install spiffile-operator oci://ghcr.io/petersr/charts/spiffile-operator \
  -n spiffile-system --create-namespace
```

Or from a checkout:

```bash
helm install spiffile-operator charts/spiffile-operator \
  -n spiffile-system --create-namespace
```

Or raw manifests:

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/operator.yaml   # set the image first
kubectl apply -f deploy/webhook.yaml    # optional pod injection (needs cert-manager)
```

Released images are multi-arch (amd64/arm64), keyless-signed and carry SLSA
provenance + SBOM — verify before trusting:

```bash
cosign verify ghcr.io/petersr/spiffile-operator:0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/PeterSR/spiffile-operator/.+@refs/tags/v0.1.0'
```

## Quick start

With the operator installed, declare an identity and watch it become ready:

```bash
kubectl create namespace shop
kubectl apply -f - <<EOF
apiVersion: spiffile.io/v1alpha1
kind: ServiceIdentity
metadata:
  name: billing
  namespace: shop
spec:
  trustDomain: example.org
EOF

kubectl get serviceidentities -n shop
# NAME      SPIFFE ID                      READY   AGE
# billing   spiffe://example.org/billing   true    5s
```

The operator now maintains Secret `billing-spiffile` and ConfigMap
`spiffile-bundle` in `shop`. Mount them as shown [above](#spiffile-operator) —
or skip the mounting boilerplate entirely with [pod
injection](#pod-injection-optional). Key rotation is one command away:

```bash
kubectl annotate serviceidentity billing -n shop \
  spiffile.io/rotate="$(date -Is)" --overwrite
```

## Pod injection (optional)

Without injection, workloads mount the Secret/ConfigMap explicitly (above).
With the webhook enabled (`--set webhook.enabled=true`, requires
cert-manager), workloads never reference the generated names — they declare
*which identity they run as* and the webhook injects the volumes, mounts and
`SPIFFILE_*` env vars:

```yaml
# explicit identity:
metadata:
  annotations:
    spiffile.io/identity: billing

# or by convention — identity = the pod's service account name:
metadata:
  annotations:
    spiffile.io/inject: "true"
spec:
  serviceAccountName: billing
```

Injection only applies in namespaces labeled `spiffile.io/injection: enabled`.
A pod requesting a non-existent identity is rejected at admission with a
clear error. Explicit mounting keeps working regardless — injection is sugar
on the same files.

## ServiceIdentity reference

| Field | Default | Meaning |
|---|---|---|
| `spec.trustDomain` | — (required) | SPIFFE trust domain |
| `spec.path` | object name | SPIFFE ID path: `spiffe://<trustDomain>/<path>` |
| `spec.secretName` | `<name>-spiffile` | name of the identity Secret |
| `spec.rotationOverlap` | `24h` | how long rotated-out public keys stay in the bundle |

Status shows the resolved `spiffeID`, current `keyID` and readiness:

```bash
kubectl get serviceidentities -A
NAMESPACE  NAME      SPIFFE ID                       READY  AGE
shop       billing   spiffe://example.org/billing    true   2d
```

## Rotation & revocation

Rotate a key by changing the rotate annotation to any new value:

```bash
kubectl annotate serviceidentity billing -n shop \
  spiffile.io/rotate="$(date -Is)" --overwrite
```

The operator generates a new keypair, updates the Secret, and keeps the old
public key in the bundle for `rotationOverlap` so in-flight tokens and
not-yet-refreshed pods keep verifying — then prunes it.

Revoke an identity by deleting its `ServiceIdentity`: its keys disappear
from every bundle on the next sync; mounted bundles propagate via the
kubelet within about a minute.

## Trust model

Identity issuance authority = RBAC on `ServiceIdentity` objects (plus read
access to the generated Secrets, which follow normal Secret RBAC). Whoever
can create a `ServiceIdentity` in a namespace can mint identities; nobody
else can — there are no other credentials to leak. The bundle ConfigMap is
public key material.

## Multi-cluster

Bundles and keys are ordinary Secrets/ConfigMaps, so existing
secret-federation machinery applies — e.g.
[External Secrets Operator](https://external-secrets.io) `PushSecret` to
publish a bundle to a shared store and `ExternalSecret` to pull it in other
clusters.

For identities whose authority lives **outside** the cluster, use a
**`ServiceIdentityClaim`** (PV/PVC-style): a courier (e.g. ESO) delivers the
key Secret per namespace and the bundle once into a shared namespace; the
operator validates, fans the bundle out, and reports status — read-only,
no store SDKs, no polling. Every consuming cluster is an equal replica; a
writer built on the spiffile libraries owns provisioning and rotation.
A trust domain is either cluster-backed or externally-backed, never both
(enforced). See [docs/store-backend.md](docs/store-backend.md).

```yaml
apiVersion: spiffile.io/v1alpha1
kind: ServiceIdentityClaim
metadata: { name: tenant-manager, namespace: shop }
spec:
  trustDomain: platform.example
  source:
    delivered:
      bundleFrom:
        configMapRef: { name: shared-bundle, namespace: spiffile-system }
```

## Development

```bash
make test    # go test ./...
make lint    # gofmt + go vet (+ golangci-lint if installed)
make build   # compile into bin/
```

Profile primitives (key generation, JWKs, bundle documents) come from the
[spiffile Go library](https://github.com/PeterSR/spiffile/tree/main/go).
Its test suite pins JWK/thumbprint output to fixtures generated by the
Python reference implementation, so implementations can't drift.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full development setup and
pull-request expectations.

## Community & security

Contributions are welcome — start with [CONTRIBUTING.md](CONTRIBUTING.md).
This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).

Security issues: please report privately per [SECURITY.md](SECURITY.md),
never as public issues.

## License

[Apache-2.0](LICENSE)
