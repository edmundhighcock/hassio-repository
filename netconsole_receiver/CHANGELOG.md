# Changelog

## 0.1.3

- Default `ata_error` regex now also matches `exception Emask` and
  `DID_TIME_OUT` (anywhere in the message, not only after `ataN:`), so a disk
  stall pages on its first line. Installed options keep their old regex:
  edit `ata_error` in the add-on options to pick it up. These two strings are
  never swallowed by the softreset suppression.
- Resume detection for the benign-softreset rule no longer depends on the
  `PM: suspend exit` line, which on a real resume falls inside the sequence
  GAP. Resume markers are now: that line, a sequence/msgid GAP, and the first
  message after >= 5 min of silence that follows `sys-netconsole: suspending`.
  Each opens the 60 s window. The 30 s link-up fallback is unchanged. Tested
  against the real 2026-11-16 resume log. No new options.

## 0.1.2

- Benign SATA softreset at resume no longer raises `ata_error`. A match whose
  only matching line is `ataN: softreset failed` is dropped when it comes
  within 60 s after a `PM: suspend exit` line, and is otherwise held back for
  30 s: a `SATA link up` for the same port inside that time drops it, if none
  comes it alerts as before (30 s late). Every other matching line (exception
  Emask, failed command, hard resetting link, timeouts, ...) alerts at once.
  No new options.
- Documented (and tested) that the add-on never re-emits an event at startup:
  the event topic is not retained and nothing is published to it on connect.
  The "replay" seen on 2026-11-22 was Home Assistant restoring the event
  entity's last state when the add-on's availability flipped back to online;
  guard the automation with `trigger.from_state` and the event timestamp.

## 0.1.1

- Removed the `listen_port` option: the receiver always listens on 26666/udp
  inside the container, and the host port is changed in the Network tab. An
  old `listen_port` in options.json is ignored. (Bumped rather than amended
  to keep the options history honest.)

## 0.1.0

First release.

- Receives Linux netconsole datagrams (extended and plain, with the optional
  release prefix, `ncfrag` fragments and userdata) on UDP, restricted to a
  `sources` allow-list; everything else is dropped and counted.
- One rotated log file per source, written by the add-on itself, kept out of
  Home Assistant backups.
- Sequence tracking per source (`seq` and `msgid`): `GAP` and `SEQ_RESET`
  lines.
- Alert rules with a per-rule cooldown: first match fires at once, one summary
  when the window closes.
- MQTT discovery: one device per source (last message, messages, sequence
  gaps, last alert, problem, alert event) and one for the receiver
  (dropped datagrams), with an availability topic and last-will.
