# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Until v1.0.0, minor versions may contain breaking changes to the `v1alpha1` API.

## [Unreleased]

## [0.1.2] - 2026-06-07

### Fixed

- Deleting the last `ServiceIdentity` in a namespace now prunes the
  namespace's `spiffile-bundle` ConfigMap, so revocation by deletion
  propagates instead of leaving a stale bundle that still trusts the
  revoked identity. Data keys owned by claims are preserved. (#5)

## [0.1.1] - 2026-06-06

### Changed

- Depend on the released spiffile library v0.0.2.
- Update to the Kubernetes 1.36 client libraries and controller-runtime 0.24.
- Build release images with Go 1.26.

### Added

- Artifact Hub annotations and repository metadata for the Helm chart.
- README quick start and image signature verification examples.

## [0.1.0] - 2026-06-06

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

[Unreleased]: https://github.com/PeterSR/spiffile-operator/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/PeterSR/spiffile-operator/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/PeterSR/spiffile-operator/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/PeterSR/spiffile-operator/releases/tag/v0.1.0
