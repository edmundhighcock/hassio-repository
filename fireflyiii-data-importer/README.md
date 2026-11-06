# Home Assistant add-on: Firefly III Data Importer (Fin)

A fork of alexbelgium's [`fireflyiii_data_importer`](https://github.com/alexbelgium/hassio-addons/tree/master/fireflyiii_data_importer)
add-on, rebuilt locally with a pinned upstream image, a household-specific
duplicate-detection patch, and a built-in import mutex. See `CHANGELOG.md`
for the exact diff from alexbelgium's recipe, and `patches/README.md` for
the patch itself.

## Why this fork exists

Lunch Flow (the Open Banking aggregator this household uses to feed
Firefly III) gives the **same `external-id`** to a transaction on two
different accounts it reports on both sides of — most visibly an
inter-account transfer, which shows up once on each account's own feed.
The Data Importer's `cell`/`external-id` duplicate-detection mode is
**ledger-wide**: it sees the id has already been imported (on the *other*
account) and silently drops the second leg as a duplicate, rather than
importing it against the account it actually belongs to.

This fork's patch scopes that duplicate check to the account the row is
being imported onto, so two legs that happen to share an `external-id`
but land on different Firefly III accounts are both imported.

## What changed vs. alexbelgium's add-on

1. **Base image pinned.** `fireflyiii/data-importer:version-2.3.5` instead
   of `:latest`, for both `amd64` and `aarch64`, so a rebuild cannot pick
   up a newer upstream release without someone deciding to.
2. **The dedup patch.** Applied at build time from
   `patches/account-scoped-dedup.patch` (a `git diff` against upstream tag
   `v2.3.5`) with `patch -p1 --forward`; the build fails if a non-empty
   patch does not apply cleanly.
3. **The import mutex is baked in**, not installed by a cont-init script
   at every container start. `/usr/local/bin/importer-locked-run` wraps
   the auto-import artisan command in a non-blocking `flock -n -E 0` on
   `/tmp/firefly-importer.lock`; `rootfs/templates/cronupdate`'s silent
   and verbose branches both call it. This prevents the concurrent-run
   duplicate creation that a bare read-then-write dedup is otherwise
   exposed to (cron firing while a manual import is already running).
4. **No build-time fetch from alexbelgium's repo.** The upstream template
   scripts `ha_automodules.sh`/`ha_autoapps.sh` used to curl from
   `alexbelgium/hassio-addons`' unpinned `master` branch are vendored
   under `vendor/.templates/` instead — see `vendor/README.md` for
   provenance — so a build no longer depends on that repository.
5. **The `devices:` list is gone** from `config.yaml` — hardware
   passthrough nodes a pure web-app/import addon never uses.

Everything else — options, schema, ports, ingress, the `map:` mounts, the
S6/bashio bootstrap machinery, the healthcheck — is alexbelgium's,
unchanged, so an existing configuration under `/config` (or
`/addon_configs/.../fireflyiii_data_importer/`) can be copied across to
this add-on's config location without translation.

## Configuration

Same options as upstream: `FIREFLY_III_URL`, `FIREFLY_III_ACCESS_TOKEN`,
`CONFIG_LOCATION`, `Updates`, `silent`, and the rest — see alexbelgium's
[upstream documentation](https://docs.firefly-iii.org/data-importer) and
his add-on's own README for the full option reference; nothing there
changed here.

### `DUPLICATE_DETECTION_SCOPE_TO_ACCOUNT`

The patch's own behaviour is controlled by this environment variable,
**default `true`**. Set it through the add-on's existing generic
`env_vars` option (no schema change needed):

```yaml
env_vars:
  - name: DUPLICATE_DETECTION_SCOPE_TO_ACCOUNT
    value: "true"
```

Setting it to `false` restores upstream's ledger-wide dedup behaviour,
for comparison or rollback without rebuilding the image.

## Licence

Inherited from alexbelgium's add-on: MIT, `Copyright (c) 2021 alexbelgium`
— see `LICENSE`. This fork adds no separate copyright claim.
