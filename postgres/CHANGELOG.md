# Changelog

## [13.9-p4] - 2026-04-04

### Changed

- Renamed `initial_password` config option to `password` (backwards compatible with `initial_password`)
- Password is now synchronized to the database on every restart, allowing password changes from the HA UI
- Fixed symlink creation that could fail on addon restart

## [13.9] - 2023-02-08

### Added

- Created simple addon which provides a postgres server
- postgres version 13.9
