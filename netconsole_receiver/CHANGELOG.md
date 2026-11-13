# Changelog

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
