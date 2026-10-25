# Static Sites

Serves the household's static sites — HTML, JavaScript, CSS and JSON, with
no backend — through Home Assistant **Ingress only**. Every request passes
Home Assistant's own login; the add-on publishes no host port, and nginx
accepts connections from the Supervisor's ingress gateway alone.

Every Home Assistant user, admin or not, can open it, so every site has the
same audience. A site needing a narrower audience needs its own add-on.

## Where sites live

On the host: `/addon_configs/9547a9e0_static_sites/sites/<name>/`
(inside the container: `/config/sites/<name>/`). `<name>` is lower-case
letters, digits, `-` and `_`, starting with a letter or digit. Creating the
directory creates the site; no restart is needed.

```
sites/<name>/
  releases/2026-10-25T0600/   a complete release
  releases/2026-10-26T0600/
  current -> releases/2026-10-26T0600     RELATIVE symlink
```

Only `current` is served. `releases/` is never reachable directly.

## Publishing a release (atomic)

Upload the complete release first, then swap the symlink in one rename, so
no viewer ever sees a half-written site:

```sh
cd /addon_configs/9547a9e0_static_sites/sites/<name>
ln -s releases/<release> current.new && mv -T current.new current
```

The symlink must be **relative** (`releases/...`): the host path and the
container path differ, so an absolute target would dangle inside the
container. nginx re-resolves `current` on every request, so the new release
appears on the next browser refresh — no restart. If `current` is absent or
dangling, the site's front page shows "Nothing has been published here yet"
and other paths return 404. Pruning old releases is the publisher's job.

## URLs inside a site

Ingress serves the panel under a per-session path prefix, so **every URL in
a site must be relative** (`data/summary.json`, never `/data/summary.json`).
A site is at `<name>/`; the front page lists the sites, or opens the only
one. Going straight to a site's URL bypasses that.

## Shared chart library

ECharts 6.1.0 (Apache-2.0) is baked into the image at `vendor/echarts-6.1.0/echarts.min.js`.
From a site's `index.html` that is `../vendor/echarts-6.1.0/echarts.min.js`. It is cached as immutable; a
new version will arrive at a new path.

## What the server enforces

- GET and HEAD only; no directory listings.
- `Cache-Control: no-cache` on `.html`, `.json` and directory URLs.
- A Content-Security-Policy allowing scripts, styles, data, fonts and
  images from this site only (plus inline scripts and styles, and `data:`
  and `blob:` images). **A page cannot load anything from the internet** —
  a CDN reference fails — so pages also work with no internet connection.
