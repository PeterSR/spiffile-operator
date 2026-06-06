# Security Policy

spiffile-operator manages workload identities and private key material, so
security reports get priority attention.

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.**

Report privately via GitHub's security advisory form:

> https://github.com/PeterSR/spiffile-operator/security/advisories/new

You should receive an initial response within a few days. Please include a
description of the issue, reproduction steps if possible, and the affected
version. You'll be credited in the advisory unless you prefer otherwise.

## Supported versions

Pre-1.0, only the **latest release** receives security fixes.

## Scope notes

The trust model is documented in the [README](README.md#trust-model): identity
issuance authority equals RBAC on `ServiceIdentity` objects, generated Secrets
follow normal Secret RBAC, and the bundle ConfigMap is public key material.
Reports that demonstrate a way to cross those boundaries — minting or reading
identities without the corresponding RBAC, making the operator copy data out
of namespaces other than the shared-bundle namespace, or breaking trust-domain
exclusivity — are exactly what this policy is for.
