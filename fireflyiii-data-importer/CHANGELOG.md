# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This add-on is a fork of
[alexbelgium/hassio-addons](https://github.com/alexbelgium/hassio-addons)'s
`fireflyiii_data_importer` add-on (MIT licensed), which packages
[firefly-iii/data-importer](https://github.com/firefly-iii/data-importer)
for Home Assistant. All credit for the base image, the S6/bashio
integration machinery and the original add-on design belongs to
alexbelgium; this fork exists to add one household-specific patch and to
pin the build against drift.

## [2.3.5-fin1] - 2026-11-05

### Changed

- Base image **pinned** to `fireflyiii/data-importer:version-2.3.5` (both
  `amd64` and `aarch64`), instead of alexbelgium's `:latest`, so a rebuild
  of this add-on cannot silently pick up a newer upstream data-importer
  release.
- Baked in the flock-based import mutex that the live add-on installs at
  every container start via a cont-init autoscript
  (`skills/firefly-import-common/scripts/fireflyiii-data-importer-autoscript.sh`
  in Fin's repo): `/usr/local/bin/importer-locked-run` now ships as part
  of the image, and `rootfs/templates/cronupdate`'s two artisan
  invocations (silent and verbose) both go through it. Same lock file
  (`/tmp/firefly-importer.lock`) and the same non-blocking `flock -n -E 0`
  semantics — a second run that finds the lock held exits 0 (a no-op), so
  cron sees a clean skip rather than a failure.

### Added

- Build-time application of `patches/account-scoped-dedup.patch`, a
  `git diff`-format patch against upstream tag `v2.3.5`, applied with
  `patch -p1 --forward` against `/var/www/html` before any of
  alexbelgium's own image modifications run. The build fails if a
  non-empty patch does not apply cleanly. Scopes the data importer's
  duplicate detection to the account a transaction is being imported
  onto, instead of the whole Firefly III ledger — Lunch Flow (the Open
  Banking aggregator this household uses) assigns the SAME `external-id`
  to a transaction on two different accounts it reports on both sides of
  (e.g. an inter-account transfer visible from each account's feed), and
  ledger-wide `cell`/`external-id` dedup drops the second leg as a
  duplicate instead of importing it.
  See `patches/README.md` — the patch file itself is a placeholder
  pending a separate agent's work; an empty placeholder skips the patch
  step so the add-on still builds.

[2.3.5-fin1]: https://github.com/edmundhighcock/hassio-repository/tree/main/fireflyiii-data-importer
