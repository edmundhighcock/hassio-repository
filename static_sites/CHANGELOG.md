# Changelog

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
