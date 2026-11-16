#!/usr/bin/env python3
"""Add-on entry point: read /data/options.json, drop root, run taiga-mcp.

Same as `taiga-mcp serve --host 0.0.0.0 --port 8095` except that uvicorn's
log level comes from the `log_level` option (the CLI hard-codes "info").
Every option is optional; a missing key falls back to its default.
"""
import json
import os
import pwd
import sys

OPTIONS = os.environ.get("OPTIONS_FILE", "/data/options.json")
DEFAULTS = {
    "taiga_url": "http://9547a9e0-taiga:8001",
    "project_id": 1,
    "log_level": "info",
}
LEVELS = {"debug", "info", "warning", "error"}


def main() -> None:
    opts = dict(DEFAULTS)
    try:
        with open(OPTIONS) as f:
            opts.update({k: v for k, v in json.load(f).items() if v not in (None, "")})
    except FileNotFoundError:
        print(f"{OPTIONS} not found; using defaults", file=sys.stderr)

    level = str(opts["log_level"]).lower()
    if level not in LEVELS:
        print(f"unknown log_level {level!r}; using info", file=sys.stderr)
        level = "info"

    os.environ["TAIGA_URL"] = str(opts["taiga_url"]).rstrip("/")
    os.environ["TAIGA_PROJECT_ID"] = str(int(opts["project_id"]))

    if os.getuid() == 0:
        user = pwd.getpwnam("taigamcp")
        os.setgroups([])
        os.setgid(user.pw_gid)
        os.setuid(user.pw_uid)
        os.environ["HOME"] = "/tmp"

    import uvicorn
    from taiga_mcp.server import Config, build_app

    cfg = Config.from_env(host="0.0.0.0", port=8095)
    print(f"taiga-mcp: Taiga at {cfg.taiga_url}, default project "
          f"{cfg.default_project_id}, log level {level}", flush=True)
    uvicorn.run(build_app(cfg), host=cfg.host, port=cfg.port, log_level=level)


if __name__ == "__main__":
    main()
