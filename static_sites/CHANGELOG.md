# Changelog

## 0.3.0

- Every page now carries a small floating Back / Refresh / Home control,
  injected server-side (nginx `sub_filter` on `</body>`) so site authors
  need not add anything. Back uses browser history, Refresh reloads the
  current page, and Home returns to this add-on's own front page (the
  ingress root, worked out from the page's URL at runtime). It respects
  light and dark mode, is namespaced (`ha-nav-*`) so it cannot collide
  with a site's own scripts, and adds nothing outside the existing
  Content-Security-Policy (inline-only, no external requests).

## 0.2.0

- The front page is now always a site browser: one card per site, titled
  from the site's optional `site.json` (`title`, `description`), with
  unpublished sites labelled. It no longer jumps into a lone site.

## 0.1.0

- First release: nginx serving `sites/<name>/current` from the add-on's
  config directory through Ingress only, with atomic symlink-swap releases,
  a site-listing front page, a placeholder for unpublished sites, a strict
  Content-Security-Policy, and ECharts 6.1.0 vendored at
  `vendor/echarts-6.1.0/`. Local test harness in `tests/test.sh`.
