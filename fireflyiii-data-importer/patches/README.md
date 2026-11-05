# patches/

`account-scoped-dedup.patch` is a placeholder — it is **empty on purpose**
and is being produced by another agent, not by this commit. It is meant to
land here as a `git diff`-format patch against upstream tag `v2.3.5` of
[firefly-iii/data-importer](https://github.com/firefly-iii/data-importer),
with paths `a/app/...` / `b/config/...`, scoping duplicate detection to the
account a transaction was imported onto rather than the whole ledger (see
the top-level README.md for why).

The Dockerfile applies this file with `patch -p1 --forward` against
`/var/www/html` inside the image and fails the build if it does not apply
cleanly — **except** that an empty file is treated as "no patch yet" and
the step is skipped, so the add-on still builds (without the dedup fix)
until the real patch is dropped in here. Once it is, the build becomes a
hard gate: a patch that does not apply cleanly against the pinned
`version-2.3.5` base image must stop the build, not degrade silently.
