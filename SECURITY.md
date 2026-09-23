# Security Policy

## Supported versions

Only the latest minor release receives security fixes.

## Reporting a vulnerability

Please **do not** open a public issue. Use GitHub's
[private vulnerability reporting](https://github.com/neverlless/google2ipa/security/advisories/new)
instead. You will get an answer within 7 days. A fix and advisory are published
within 90 days, or sooner once a fix is available.

## Operating google2ipa safely

- Give the FreeIPA service account only the role from
  [docs/freeipa-setup.md](docs/freeipa-setup.md), not `admin`.
- The Google service account needs only the two read-only Directory scopes.
  Prefer keyless Workload Identity over key files.
- Welcome mails contain one-time passwords. Use an SMTP server with STARTTLS,
  or keep `notify.welcome.enabled: false` and hand out passwords another way.
- Keep `freeipa.insecure_skip_verify` off outside of labs; use `freeipa.ca_file`.
- Verify release images with `cosign verify` (see README).
