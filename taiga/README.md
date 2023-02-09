# hass-taiga
An add-on to allow the agile Taiga project manager to be deployed on home assistant

## What works

- Full Taiga 6 installation with events
- Visible in the web ui which can be added to the Home Assistant sidebar.
- Data persistence
- Manual backups
- Architecture: `amd64` (i.e. most modern Intel and AMD chips)

## What still needs to be done

- Integration with Home Assistant, e.g. a hass card with events in todo
- Integration with Home Assistant user accounts
- Allow use of a 3rd-party postgres server
- Automatic backups
- Architecture: `arm` (i.e. Raspberry Pi). Contributions welcome!
- Use Home Assistant ubuntu base images + `bashio`
- Shrink addon installation size (currently 2GB)

## System requirements

Taiga is a big application so needs a bit of power behind it.

- Around 3GB disk space.
- Around 400MB RAM
- Architecture=`amd64` (extending to ARM is a high priority)


## Installation

Installation requires the Home Assistant OS, i.e. you are not using
hass in a container.

### Add the repository

1. Go to Settings -> Addons -> Addon Store -> Kebab (3 dots) menu -> Repositories
2. Add this repository: https://github.com/edmundhighcock/hassio-repository
3. Go to Settings -> Addons -> Addon Store -> Kebab (3 dots) menu -> Check for Updates
4. Refresh the page

You should now see the repository which should  include these 3 addons:

1. Hass Addon for RabbitMQ
2. Hass Addon for Postgres
2. Taiga Project Manager

### Install services

1. Install the `Addon for RabbitMQ`
2. Ensure "Start on Boot" and "Watchdog" are checked.
3. Install the `Addon for Postgres`
4. Ensure "Start on Boot" and "Watchdog" are checked.

### Install Taiga

1. Install Taiga. This will take some time (depending on your internet speed) as it is a 2GB image (this will be optimised further in the future, but even so Taiga is a big application).
2. **Important**. Go to the configuration page and set the taiga secret key to a secure random passphrase. It is really important to do this before starting Taiga
3. Select "Show in Sidebar" if desired.
3. Start Taiga
4. **Important**. Now go to the Taiga web ui, and log in as `taiga_admin` with password "pleasechangeme". Immediately go to the Taiga settings (click the logo in the top right corner) and change the password to secure your installation.
