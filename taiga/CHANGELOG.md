# Changelog

## [6.0.10-p8] - 2026-11-03

### Changed

- **Build**: replaced the deprecated `build.yaml` + `ARG BUILD_FROM` /
  `FROM $BUILD_FROM` pattern with per-architecture `Dockerfile.amd64` /
  `Dockerfile.aarch64`, each with a fixed `FROM`. Since Supervisor
  2026.04.0, `BUILD_FROM` is only passed when a `build.yaml` is present
  (and then only with a deprecation warning); a bare `ARG BUILD_FROM` with
  no default, as this add-on had, breaks when `build.yaml` is removed, and
  `build.yaml` itself is being phased out. Per-arch Dockerfiles are the
  documented replacement when different architectures need genuinely
  different base images (not just an arch-selection macro). Confirmed
  against:
  - https://contributors.home-assistant.io/apps/guides/dockerfile/#build-arguments
  - https://contributors.home-assistant.io/apps/guides/dockerfile/#buildyaml
  - `home-assistant/supervisor` source, `supervisor/apps/build.py`
    (`AppBuild._read_build_config`, `AppBuild.base_image`,
    `AppBuild.get_docker_args`) and `tests/apps/test_build.py`
    (`test_no_build_yaml_no_build_from_arg`,
    `test_build_yaml_passes_build_from`): `BUILD_ARCH` and `BUILD_VERSION`
    are always passed; `BUILD_FROM` is only added to the build args when a
    `build.yaml`/`build.json` exists.
- **Architecture**: `arch` is now `[aarch64, amd64]` (dropped `armv7`,
  which has no published base image and which Home Assistant OS itself no
  longer supports on 32-bit ARM). `armv7` is also one of the arch values
  the current add-on config docs call out as deprecated-but-still-accepted.
- `ingress: 8099` corrected to `ingress: true`. `ingress` is documented as
  a `bool`; the Supervisor's schema coerces any non-empty/non-zero value
  to `True` (voluptuous `Boolean()` casts non-string values with
  `bool(v)`), so this happened to work, but it is not what the key means -
  the actual ingress port is `ingress_port`, already set separately.

### Added

- Scale-control options (defaults sized for a light household install -
  a couple of humans and a few agents - on a Raspberry Pi 5, see
  `translations/en.yaml` for the full trade-offs of each):
  - `gunicorn_django_workers` (default 2) / `gunicorn_django_threads`
    (default 2, via `--worker-class gthread`) / `gunicorn_django_max_requests`
    (default 500, ±10% jitter, 0 disables) for the Django API (port 8001),
    replacing the previous hard-coded `--workers 4` with no `--threads` or
    `--max-requests`.
  - `celery_enabled` (default true), `celery_concurrency` (default 1),
    `celery_pool` (default `solo`; `prefork`/`threads` also available)
    for the Celery worker, replacing the hard-coded `--concurrency 4`
    (implicit `prefork`). Beat stays embedded (`-B`) regardless of pool:
    Celery's own docs (periodic-tasks guide) only call embedded beat
    unsuitable for setups with *more than one worker node*, which does not
    apply to this single-worker add-on, and no pool-specific
    incompatibility with `-B` is documented for `solo`/`threads`/`prefork`.
    Verified against `docs.celeryq.dev/en/v5.2.3/userguide/periodic-tasks.html`
    and `userguide/workers.html` for the installed Celery 5.2.3.
    When `false`, `run.sh` does not start Celery at all, and Django is also
    told `CELERY_ENABLED=False` (see `backend_config.py` - this already
    existed as a read `os.getenv`, just never wired to anything).
  - `taiga_protected_workers` (default 1) for the signed-attachment-URL
    service (port 8003), replacing the hard-coded `--workers 4`.
  - `events_enabled` (default true): when false, `run.sh` does not start
    `taiga-events` (the Node WebSocket push service). RabbitMQ itself is
    untouched either way, including for Django's own direct event
    publishing (see below).
  - `malloc_arena_max` (default 2, 0 = leave glibc's default in place),
    exported before every Python process is launched (Django, Celery,
    protected-media all fork from the same `run.sh` environment).
- `translations/en.yaml` with a name and description for every new
  option, including the functional trade-offs of turning Celery or events
  off.
- `tests/`: a syntax check (`bash -n`) plus a harness
  (`tests/test.sh`) that runs `start.sh`/`run.sh` for real, as the real
  `taiga` user via the real `su -w`/`jq` from the upstream base image,
  against several option combinations, and asserts the exact gunicorn/
  celery/npm/nginx command lines produced.

### Verified

- Live events (the WebSocket push that keeps open browser tabs in sync)
  are published by Django directly to RabbitMQ, independent of Celery.
  `taiga/events/events.py` (`emit_event`) calls
  `backends.get_events_backend().emit_event(...)` synchronously (via
  `connection.on_commit`), and
  `taiga/events/backends/rabbitmq.py` publishes straight to RabbitMQ with
  the `amqp` library's `Connection`/`basic_publish` - there is no
  `.delay()`/Celery task anywhere in that path. Confirmed by reading both
  files, plus `taiga/events/signal_handlers.py`, inside the
  `edmundhighcock/hass-taiga:withjq` base image.

## [6.0.10-p5] - 2023-02-12

### Fixed

- Automatic backup of multiple projects

## [6.0.10-p4] - 2023-02-12

### Added

- Automatic backup of all projects to the Home Assistant 'share' folder

## [6.0.10-p3] - 2023-02-12

### Added

- Configurable postgres name, user, host and database.

## [6.0.10-p2] - 2023-02-10

### Fixed

- Panel icon

## [6.0.10-p1] - 2023-02-10

### Added

- Panel icon

## [6.0.10] - 2023-02-08

### Added

- Full taiga installation with events.
- Taiga version 6.0.10
