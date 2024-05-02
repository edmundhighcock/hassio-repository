# hass-taiga
An add-on to allow the agile Taiga project manager to be deployed on home assistant

## What works

- Full Taiga 6 installation with events
- Visible in the web ui which can be added to the Home Assistant sidebar.
- Data persistence
- Architecture: `amd64` (i.e. most modern Intel and AMD chips) and `aarch64` (Raspberry PI)
- Allow use of a 3rd-party postgres server (must be <= 13.9)
- Automatic backups
- Access the WebUI via the nabucasa remote URL (`https://<uid>.ui.nabu.casa/...`)


## What still needs to be done

- Integration with Home Assistant, e.g. a hass card with events in todo
- Integration with Home Assistant user accounts
- Use Home Assistant ubuntu base images + `bashio`
- Shrink addon installation size (currently 2GB)
- Configuring email notifications
- Accessing the WebUI via the local URL (`https://homeassistant.local/...`)

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
3. Go to the configuration section and choose a password.
4. Start the addon.
5. Install the `Addon for Postgres`
6. Ensure "Start on Boot" and "Watchdog" are checked.
7. Go to the configuration section and choose a password.
8. Start the addon.

_Important Note: You can change the RabbitMQ password at any time if you restart the addon. However, once you set the initial Postgres password, you need to use the command line to change it (instructions will be added to the Postgres addon soon)._

### Install Taiga

1. Install Taiga. This will take some time (depending on your internet speed) as it is a 2GB image (this will be optimised further in the future, but even so Taiga is a big application).
2. **Important**. Go to the configuration page and set the taiga secret key to a secure random passphrase. It is really important to do this before starting Taiga
3. Add the passwords for RabbitMQ and Postgres that you set earlier.
4. Select "Show in Sidebar" if desired.
5. Start Taiga
6. **Important**. Now go to the Taiga web ui, where you will already be logged in as `taiga_admin` (with password "pleasechangeme"). Immediately go to the Taiga settings (click the logo in the top right corner) and change the password to secure your installation.

## Administration

When you install Taiga there is one superuser account created. It is probably not ideal to use that for your day-to-day work. You can create an ordinary user account, as well as carry out other admin tasks, in the admin section.

Taiga does not include a link to the admin section in the frontend. So this is how you get to it.

1. In the Taiga application, right-click on any link and select "Open in New Tab" (or equivalent on your browser)
2. The new tab will have a URL that begins like this: `https://<your hass server>/api/hassio_ingress/<longkey>/`, where the `<longkey>` is a random sequence of numbers and letters.
3. Modify the URL to look like this (note the trailing slash):  `https://<your hass server>/api/hassio_ingress/<longkey>/admin/`
4. You should now see the Django admin page

### Backup and Restore

The addon regularly exports all projects into the home assistant `shared` folder. This means that when you do a full backup of Home Assistant (which includes the `shared` foler) you will save a snapshot of all your projects at that time. 

Each project is exported as a giant json file. 

If the worst happens and you need to restore the addon from scratch, you can 

1. download your home assistant backup,
2. open up the tarball,
3. find the shared folder tarball, and
4. open the tarball and find the `9547a9e0-taiga` folder.
5. For each project json file, you can create a new project in taiga, and select the "Import Project" option, then upload the json file to recreate your project.

#### Changing the update frequency

You can change the time between project exports by changing the parameter `minutes_between_backups` in the project configuration.


