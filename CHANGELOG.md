# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-09-23

### Added

- `sync.max_username_length` (default 32): too-long usernames are skipped with a clear error.
- Preserved (deleted) FreeIPA users that reappear in Google are reported with the restore command.

### Fixed

- A user whose managed-group assignment fails right after creation is rolled back instead of being orphaned.
- Group changes are no longer applied to users whose adoption failed.
- The same persistent errors are mailed once instead of on every `--interval` pass.
- FreeIPA retry waits stop when the pass is cancelled or times out.
- First-run lookups of existing FreeIPA users run in parallel.
- The unset-variable config error names each variable once.

## [0.1.0] - 2026-09-23

Initial public release.

### Added

- One-way sync of Google Workspace users into FreeIPA / Red Hat IdM over the JSON-RPC API.
- Service-account authentication with domain-wide delegation, by key file or keyless (Workload Identity).
- Full pagination; filters by Directory API query, organizational unit and email domain.
- Google group → FreeIPA group mapping, including nested Google groups.
- FreeIPA-generated one-time passwords and optional welcome mail.
- Offboarding: lock, then delete (preserved by default) after a configurable delay; re-enable returning users.
- Safety brake that stops mass disabling after a partial or empty Google response.
- `--dry-run`, `--interval`, JSON or text logs, admin summary mail.
- Distroless multi-arch container image signed with cosign; Kubernetes and Docker Compose examples.

[Unreleased]: https://github.com/neverlless/google2ipa/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/neverlless/google2ipa/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/neverlless/google2ipa/releases/tag/v0.1.0
