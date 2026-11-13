# Netconsole Receiver

Receives Linux **netconsole** kernel-log datagrams over UDP, keeps rotated
per-machine log files, and publishes per-machine status and alerts to Home
Assistant as entities, through MQTT discovery. It is a general tool: every
machine name, address and rule lives in the add-on options.

Netconsole sends kernel messages (`printk`) as UDP datagrams from inside the
kernel, so it can still report a hard lockup or a panic after the machine has
stopped writing to disk. The point of this add-on is to keep the last words
of a machine that has just died, and to tell you about it.

Formats are those of the kernel documentation,
<https://docs.kernel.org/networking/netconsole.html>.

## Options

Every option except `sources` and `alert_rules` is optional; an absent or
out-of-range value takes the default shown.

| Option | Default | Meaning |
| --- | --- | --- |
| `sources` | `[]` | The allow-list: `{name, address, port?}`. See below. |
| `log_dir` | `/config` | Where the logs go. `/config` is the add-on's own `addon_config` folder. |
| `log_max_mb` | `5` | Rotate a source's log at this size. |
| `log_files` | `5` | Files kept per source, the live one included (disk use is at most `log_max_mb` x `log_files` per source). |
| `alert_rules` | four kernel rules | `{name, regex, cooldown_min?}`. See below. |
| `silence_after_min` | `15` | Published as an attribute on `Last message`, for use in the silence automation below. The add-on itself never alerts on silence. |
| `log_level` | `info` | `debug`, `info`, `warning`, `error`. |
| `go_memlimit_mib` | `24` | Soft memory limit for the Go runtime. |
| `mqtt_host`, `mqtt_port`, `mqtt_username`, `mqtt_password` | blank / `1883` | Override for the Supervisor's MQTT details. See "MQTT credentials". |

### Port

The receiver always listens on UDP 26666 inside the container; there is no
option for it. To have senders use a different port, change the HOST port in
the add-on's Network tab (the Supervisor maps it onto 26666) and set the
sender's `remote_port` to that host port.

### sources

```yaml
sources:
  - name: nas            # slug: a-z, 0-9, _  (device name, entity ids, log file name)
    address: 192.168.1.20
  - name: router
    address: 192.168.1.1
    port: 6665           # optional: only datagrams from this UDP source port
```

A datagram from an address (and port, if given) that is not listed is
dropped and counted in the receiver's `Dropped datagrams` sensor; the first
drop from each address is also logged. **An empty list accepts nothing**, and
the add-on says so loudly in its log at start-up and every ten minutes.

### alert_rules

```yaml
alert_rules:
  - name: hard_lockup        # slug; becomes an event type
    regex: "hard LOCKUP"     # Go (RE2) syntax, tested against each message
    cooldown_min: 1          # optional, default 15
```

Defaults, used when `alert_rules` is absent (and written into the options on
install): `hard_lockup` = `hard LOCKUP` and `kernel_panic` = `Kernel panic`
(1 minute), `ata_error` = `ata[0-9.]+: .*(timeout|reset|failed command)` and
`btrfs_error` = `BTRFS (error|warning|critical)` (15 minutes). To change a
regex, edit it in the options; to add a rule, append one; `[]` means no
rules. A rule with a bad regex is skipped and logged as an error.

How a rule behaves, per source:

- The **first** match fires the `alert` event and updates `Last alert` and
  `Problem` at once.
- Further matches inside the cooldown window are only counted.
- When the window closes, if the count rose, **one summary** event fires.
- `Problem` is on while any rule's window is open, and off when the last
  closes.

Alerts are matched against the whole reassembled message, userdata included.

## Entities

One Home Assistant device per source (`Netconsole <name>`), and one for the
receiver. Entity ids follow `<domain>.netconsole_<name>_<entity>`.

| Entity | Notes |
| --- | --- |
| `sensor.netconsole_<name>_last_message` | timestamp of the last accepted datagram. Attributes: `silence_after_min`, `source_address`, `source_port`. |
| `sensor.netconsole_<name>_messages` | total datagrams (`total_increasing`). |
| `sensor.netconsole_<name>_sequence_gaps` | number of `GAP` events (`total_increasing`). |
| `sensor.netconsole_<name>_last_alert` | state is `<rule>: <excerpt>` (under 255 characters, which Home Assistant requires). Attributes: `rule`, `kind` (`first`/`summary`), `count`, `first_line`, `last_line`, `time`. |
| `binary_sensor.netconsole_<name>_problem` | on while any cooldown window is open. |
| `event.netconsole_<name>_alert` | event types are the rule names; one event per first match and one per summary. Event attributes: `kind`, `count`, `excerpt`. |
| `sensor.netconsole_dropped_datagrams` | datagrams from unlisted sources (receiver device). |

Counters (`messages`, `sequence_gaps`, `last_message`, `dropped_datagrams`)
are published at most every 30 seconds, so a burst cannot flood MQTT; alerts
and `Problem` are published immediately. All entities share one availability
topic with a retained MQTT last-will: if the add-on dies, they all go
`unavailable`.

Renaming or removing a source leaves its old retained discovery entities
behind in Home Assistant; delete them from the MQTT integration.

## Log files

One file per source: `<log_dir>/<name>.log`, rotated by size by the add-on
itself (`name.log.1` is the previous one, and so on). Each line is

    2026-11-13T12:00:00.123Z 192.168.1.20:6665 6,607,22085407756,-;message text

arrival time (UTC, milliseconds), sender `ip:port`, then the raw datagram.
Newlines, other control characters and invalid UTF-8 are escaped (`\n`,
`\xNN`), so one datagram is exactly one line. Backslashes are not doubled, so
the kernel's own `\xNN` escapes stay readable; the log is for reading, not for
machine round-tripping.

Lines written by the add-on rather than the kernel:

- `GAP <from>..<to>` : `seq` numbers jumped. This is a hint, not proof of
  loss: `seq` also advances for messages netconsole never sends (for example
  below the sender's `console_loglevel`). `GAP msgid <from>..<to>` is the same
  for the `msgid` userdata counter, which counts only messages actually sent,
  and is the better signal when enabled.
- `SEQ_RESET <old>-><new>` : the number went down, so the sender rebooted.
- `REASSEMBLED seq=<n> <text>` : a message split into `ncfrag` fragments, put
  back together. The raw fragments are logged too, as they arrived.
- `FRAG_TIMEOUT seq=<n> have=<bytes>/<total> <text>` : fragments were missing
  after 5 seconds; what arrived is logged (and still matched against the
  rules).

Datagrams without a netconsole header (a non-extended target) are stored
as-is, with no sequence tracking.

### Reading the logs over SSH

With the SSH add-on, the add-on's config folder is on the host at

    /addon_configs/<repo-prefix>_netconsole_receiver/

where `<repo-prefix>` is the hash-like prefix Home Assistant gives this
repository (see the add-on's page URL, or `ls /addon_configs`). For example:

    tail -f /addon_configs/<repo-prefix>_netconsole_receiver/nas.log

### Backups

The add-on sets `backup_exclude: ["*.log", "*.log.*"]`. In the Supervisor
the exclude filter is applied to both the add-on's `/data` and its
`addon_config` folder (`/config`), so the default `log_dir` stays out of
backups. If you point `log_dir` somewhere else (for example a `/share`
mapping), it is not covered.

## Sender setup

On the machine whose kernel log you want, with the kernel built with
`CONFIG_NETCONSOLE_DYNAMIC` (most distributions) and `netconsole` loaded as a
module (`modprobe netconsole`), use configfs:

```sh
cd /sys/kernel/config/netconsole
mkdir target1
cd target1
echo eth0            > dev_name        # sending interface
echo 192.168.1.20    > local_ip        # optional; derived from the interface if unset
echo 192.168.1.10    > remote_ip       # Home Assistant
echo 26666           > remote_port     # the add-on's port
echo aa:bb:cc:dd:ee:ff > remote_mac    # the MAC of Home Assistant (or of your gateway)
echo 1               > extended        # extended format: sequence numbers, userdata
echo 1               > enabled
# optional userdata, giving the better gap signal:
echo 1 > userdata/msgid_enabled
```

Notes:

- **A target's parameters can only be changed while `enabled` is 0.** To
  change anything: `echo 0 > enabled`, change it, `echo 1 > enabled`.
  Userdata entries can be changed while enabled.
- **Pin `remote_mac`.** Without it netconsole sends to the Ethernet
  broadcast address, which loads every machine on the segment and is
  suppressed by some switches. If Home Assistant is on another subnet, use
  your gateway's MAC.
- To send from a fixed UDP source port, set `local_port`; then you can pin
  `port` in `sources`.
- `console_loglevel` on the sender decides what is sent: `dmesg -n 8` sends
  everything.
- Persist the setup across reboots with a boot script or systemd unit; the
  configfs tree starts empty at boot. The same can be set on the kernel command line
  (`netconsole=+@192.168.1.20/eth0,26666@192.168.1.10/aa:bb:cc:dd:ee:ff`; the
  `+` selects the extended format).

## Security

- **A published UDP port binds every host interface**, not only the LAN you
  have in mind: Home Assistant OS has no per-interface option. That is why
  the `sources` allow-list exists. Datagrams from unlisted addresses are
  dropped before any parsing or logging.
- **UDP is spoofable.** Anyone who can send packets that appear to come from
  a listed address can write lines into its log and trigger alerts. The log
  is a diagnostic aid, **never evidence**. Use a network boundary (firewall,
  VLAN) if that matters.
- Log text is never passed to a shell, and control characters are escaped, so
  a hostile datagram cannot forge a second log line.

## MQTT credentials

The add-on declares `services: mqtt:want`. It asks the Supervisor
(`http://supervisor/services/mqtt`) for the broker details, then falls back to
the `mqtt_*` options. If neither works it logs an `ERROR ... retrying in 15s`
line and keeps trying; **log files are still written meanwhile**.

## Example automations

Placeholders in `<angle brackets>`; replace them.

Notify a phone on an alert event:

```yaml
alias: Netconsole alert
triggers:
  - trigger: state
    entity_id: event.netconsole_<name>_alert
conditions:
  - condition: template
    value_template: "{{ trigger.to_state.state not in ['unknown', 'unavailable'] }}"
actions:
  - action: notify.<your_mobile_app_notify_service>
    data:
      title: "{{ trigger.to_state.attributes.event_type }} on <name>"
      message: >-
        {{ trigger.to_state.attributes.kind }} x{{ trigger.to_state.attributes.count }}:
        {{ trigger.to_state.attributes.excerpt }}
```

Notify when a source has gone quiet for more than N minutes (a template using `now()` is re-evaluated every minute; the
threshold is the `silence_after_min` you set; `Last message` goes `unavailable` if the add-on
itself is down, which the condition excludes so you are not told twice):

```yaml
alias: Netconsole silent
triggers:
  - trigger: template
    value_template: >-
      {% set s = states('sensor.netconsole_<name>_last_message') %}
      {{ s not in ['unknown', 'unavailable']
         and (now() - as_datetime(s)).total_seconds()
             > state_attr('sensor.netconsole_<name>_last_message', 'silence_after_min') * 60 }}
    for: "00:01:00"
actions:
  - action: notify.<your_mobile_app_notify_service>
    data:
      message: "<name> has sent no kernel messages for a while"
```

Note that a healthy machine may send nothing for long periods; netconsole
only reports kernel messages. Choose `silence_after_min` accordingly, or send
a periodic kernel message (`echo heartbeat > /dev/kmsg` from cron) if you
need a heartbeat.

## Resource use

A small static Go binary. With the default 24 MiB soft memory limit it
settles at a few MiB of resident memory. The receive buffer is requested at
4 MiB; the kernel grants at most `net.core.rmem_max`, and the add-on logs what
it actually got at start-up.
