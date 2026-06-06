# Design: external authority via ServiceIdentityClaim (replica mode)

Status: **implemented** (the claim + delivered-objects contract). The
ExternalSecrets-generating convenience layer is a possible future addition.

## Problem

The operator's `ServiceIdentity` flow is its own authority: it generates
keys into Secrets and aggregates bundles from the objects in *this* cluster.
That is exactly right for one cluster — and structurally wrong for several:
bundles only contain local identities, so multi-cluster setups degenerate
into "one authority cluster plus consumers".

## Principle

**Authority follows the resource kind** — two CRs, PV/PVC-style:

| Kind | Analogy | Authority | Operator may write key material? |
|---|---|---|---|
| `ServiceIdentity` | — | this cluster | yes — generates and owns lifecycle |
| `ServiceIdentityClaim` | PVC claiming an externally provisioned PV | external (the writer) | **no — read only** |

And crucially for simplicity: **the operator never talks to any store.**
A claim consumes identity material that is *handed to it* as ordinary
Kubernetes objects, delivered by whatever courier you already run —
External Secrets Operator, `PushSecret`/`ExternalSecret` pairs, scripts, CI:

```yaml
apiVersion: spiffile.io/v1alpha1
kind: ServiceIdentityClaim
metadata:
  name: tenant-manager
  namespace: shop
spec:
  trustDomain: platform.example
  # path: tenant-manager            # defaults to the object name
  # secretName: <name>-spiffile     # the Secret pods mount (courier delivers it)
  source:
    delivered:                      # the only implemented source mode
      bundleFrom:
        configMapRef:
          name: shared-bundle
          namespace: spiffile-system  # the shared-bundle namespace (see below)
```

The operator validates the delivered material (key parses, `id` matches the
claimed SPIFFE ID, bundle parses + is for the right trust domain + actually
contains the claimed identity), mirrors the bundle into the standard
per-namespace `spiffile-bundle` ConfigMap, and reports status. Pods consume
claims and identities identically — the webhook resolves either kind.

"Courier" throughout this document is a **role, not a component**: whatever
process makes the delivered objects appear — an ESO `ExternalSecret`, a
`PushSecret`/`ExternalSecret` pair across clusters, `kubectl` in CI, the
writer's tooling directly. The claim only defines the contract; anything
that satisfies it is the courier.

### `secretName` is a rendezvous name, and ownership never varies

`spec.secretName` is not a pointer to an existing object — it declares the
**agreed name** at which the courier must deliver the key Secret in the
claim's namespace (ESO: `target.name`). Omitting it only changes which name
is agreed (`<claim-name>-spiffile`); it implies nothing about ownership.

Ownership is decided by the *kind*, never by which fields are set:

| | Secret owner | Operator may write it | ownerReference / GC |
|---|---|---|---|
| `ServiceIdentity` | the operator | yes (create, rotate) | yes — deleted with the SI |
| `ServiceIdentityClaim` | external (the courier/writer) | **never** — read + validate only | no — deleting the claim leaves the Secret |

Until the Secret appears, the claim reports
`Ready: false — waiting for secret "<name>" to be delivered`. The binding is
verified by content, not by naming: the delivered `id` must equal the
claimed SPIFFE ID, the key must parse, and the bundle must contain the
claimed identity.

(`bundleFrom`, by contrast, IS a true pointer — the bundle lives elsewhere
(the shared-bundle namespace) and the operator copies it to where pods need
it. The key has no such indirection: pods mount the delivered Secret itself,
so delivery location and mount location collapse into one name.)

Because everything is Kubernetes objects, there is **no polling**: the
operator watches the delivered Secrets/ConfigMaps and reacts immediately.

## The shared-bundle namespace

The bundle is the same document for every consumer of a trust domain, so the
courier delivers it **once**, into the operator's shared-bundle namespace
(`SHARED_BUNDLE_NAMESPACE`, default: the operator's own namespace), and the
operator fans it out to each claiming namespace's `spiffile-bundle`
ConfigMap. Private-key Secrets remain strictly namespace-local — that is
correct, not a cost: per-consumer secret scoping is the point.

Cross-namespace `bundleFrom` references are allowed **only** into the
shared-bundle namespace. Anything else is rejected — otherwise creating a
claim would let anyone make the operator copy content out of arbitrary
namespaces.

## Trust domain exclusivity

> **A trust domain is either cluster-backed (`ServiceIdentity`) or
> externally-backed (`ServiceIdentityClaim`) in a given cluster — never
> both.**

Mixing kinds within one trust domain would create two authorities for the
same bundle document. Both reconcilers enforce this: the conflicting object
reports `Ready: false — trust domain conflict`, and the SI-side bundle
aggregation skips claimed trust domains entirely. Different trust domains
coexist freely (their bundle ConfigMap data keys merge); note that workloads
of *different* trust domains exchanging tokens is federation (a verifier
only consults its own domain's bundle) and is out of scope.

## What read-only buys

- no ownership/deletion semantics across clusters — the writer owns lifecycle
- no minting blast radius — a compromised cluster can impersonate identities
  it hosts (it has their private keys; unavoidable) but can never create new
  ones
- no create races or merge logic at the authority — there is one writer
- no asymmetry — every consuming cluster is an equal replica

## The writer

Out of scope for the operator, on purpose. In the first iteration the writer
is **a human (or controlled automation) running a context-specific CLI**
built on the spiffile libraries — they already provide the provisioning
primitives (key generation, bundle maintenance, rotation with overlap), so a
writer is thin glue: call the library, put the results where the courier
picks them up (e.g. a secrets store synced by ESO). Rotation and revocation
are writer actions; claims have no rotate annotation.

### Example: AWS Parameter Store + External Secrets Operator

Writer publishes:

```
/spiffile/<trust-domain>/bundle          # complete bundle.json (public)
/spiffile/<trust-domain>/keys/<path>     # PEM private key (SecureString)
```

Courier (per cluster): one `ExternalSecret` for the bundle into the
shared-bundle namespace; one `ExternalSecret` per hosted identity into its
namespace (template adds the `id` data key alongside `key.pem`). IAM:
replicas read, only the writer writes — **store write access = identity
authority**; state this in your threat model.

## Source modes and extensibility

`spec.source` is a discriminated union (VolumeSource-style): exactly one
member set, enforced by CEL validation. Two invariants make new modes cheap
and consumer-invisible:

1. **`spec.secretName` is always the Secret pods mount** — a mode only
   decides who fills it (a courier delivers it; a future mode has the
   operator ensure it).
2. **The bundle always lands in the namespace's `spiffile-bundle`
   ConfigMap** — a mode only decides how the document is obtained.

Reserved (specced, not implemented) members:

- `source.parameterStore` — the operator reads a store directly (region,
  prefix, auth via the pod's cloud identity). Trades the zero-SDK property
  for fewer moving parts; status can say precisely "not provisioned".
- `source.externalSecrets` — the operator *generates* the `ExternalSecret`
  objects that deliver exactly what `delivered` consumes (a `storeRef` +
  key/bundle paths). Pure sugar composing onto the same contract.

The webhook, pods, and the trust-domain exclusivity rule are unaffected by
mode — they sit below this seam.

## Migration / coexistence

Lifting a trust domain out of the cluster is **zero-downtime**, because the
lift changes no key material — no rotation, no token invalidation:

1. **Extract** — `kubectl spiffile extract <td> <outdir>` (the
   [`kubectl-spiffile`](../scripts/kubectl-spiffile) plugin; install = put it
   on your PATH) dumps the trust domain's cluster-backed identities into the
   standard writer-root layout (`bundle.json` +
   `services/<path>/{id,key.pem}` — the same layout every spiffile library's
   provision module reads/writes). Deliberately store-agnostic: push the
   result to a parameter store, a vault, sops-encrypted git — that's the
   writer's choice, not the extract's.
2. **Orphan** — `kubectl delete serviceidentity <x> --cascade=orphan`: the
   SI goes away, the Secret stays in place, pods never notice.
3. **Claim** — apply the `ServiceIdentityClaim` plus the delivery mechanism
   (e.g. an `ExternalSecret`) targeting the *same* Secret name; it overwrites
   with byte-identical content. Authority has now moved to the writer.

Both kinds coexist in one cluster across different trust domains; the file
contract is identical either way.
