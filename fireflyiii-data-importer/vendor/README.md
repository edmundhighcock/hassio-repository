# vendor/

Build-time copies of upstream template scripts that `ha_automodules.sh`
and `ha_autoapps.sh` used to fetch over the network from
`alexbelgium/hassio-addons` at `docker build` time (its `master` branch —
unpinned, so the exact bytes could change under us between builds).
Vendoring them means a build of this add-on no longer depends on that
repository being reachable or unchanged.

## Provenance

Copied verbatim from `alexbelgium/hassio-addons`, `.templates/`, at commit:

```
b5bd66c119755af40f199cbc6bb1b04563cb8f1a
2026-11-05T20:56:36+01:00
docs(ai): the scheduled routine is now the only AI automation (#3201)
```

Files, and the URL each used to replace:

- `.templates/00-banner.sh` — was `https://raw.githubusercontent.com/alexbelgium/hassio-addons/master/.templates/00-banner.sh`
- `.templates/01-custom_script.sh` — was `https://raw.githubusercontent.com/alexbelgium/hassio-addons/master/.templates/01-custom_script.sh`
- `.templates/00-global_var.sh` — was `https://raw.githubusercontent.com/alexbelgium/hassio-addons/master/.templates/00-global_var.sh`
- `.templates/01-config_yaml.sh` — was `https://raw.githubusercontent.com/alexbelgium/hassio-addons/master/.templates/01-config_yaml.sh`
- `.templates/ha_automatic_packages.sh` — was `https://raw.githubusercontent.com/alexbelgium/hassio-addons/master/.templates/ha_automatic_packages.sh`

These five are unmodified copies of the upstream files as they stood at
that commit. `ha_automodules.sh` and `ha_autoapps.sh` now `cp` them from
here instead of curling them; the Dockerfile `COPY`s this directory into
the build and removes it again once both scripts have run. Behaviour is
unchanged — the four `.templates/0*.sh` scripts still land in
`/etc/cont-init.d/` and run at container start exactly as before, and
`ha_automatic_packages.sh` still runs during the build exactly as before
(it makes its own further build-time fetches — a pinned `bashio` tarball
from `github.com/hassio-addons/bashio` and a pinned `tempio` binary from
`github.com/home-assistant/tempio` — which are versioned releases from
Home Assistant's own organisations, not `alexbelgium/hassio-addons`'s
unpinned `master`, and are out of scope for this vendoring).

## Keeping this in step

If `MODULES` in the Dockerfile ever grows beyond the four files above,
add the new upstream template here too (same provenance commit, or a
freshly recorded one) before referencing it — `ha_automodules.sh` fails
the build loudly (`exit 1`) if a named module is missing from this
directory, rather than silently falling back to the network.
