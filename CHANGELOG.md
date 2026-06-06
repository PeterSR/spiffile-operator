# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until v1.0.0, minor versions may contain breaking changes to the `v1alpha1` API.

## [Unreleased]

### Added

- `ServiceIdentity` CRD: declare an identity, get a keypair Secret and
  per-namespace trust bundle ConfigMap, with annotation-driven key rotation
  and overlap-window pruning.
- `ServiceIdentityClaim` CRD: consume externally provisioned identities
  delivered as ordinary Secrets/ConfigMaps (replica mode), with trust-domain
  exclusivity enforced between the two kinds.
- Optional pod-injection mutating webhook (cert-manager backed).
- Helm chart and raw deploy manifests.
- `kubectl-spiffile` plugin with `extract` for zero-downtime migration of a
  trust domain out of the cluster.

[Unreleased]: https://github.com/PeterSR/spiffile-operator/commits/main
