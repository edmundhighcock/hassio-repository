# hass-fava
An add-on to work with Fava + Beancount personal accounting system.

## What works

- Working fava installation
- Beancount file in shared folder


## What still needs to be done

- Integration with Home Assistant, i.e. getting financial data on Hass dashboards


## Installation

Installation requires the Home Assistant OS, i.e. you are not using
hass in a container.

### Add the repository

1. Go to Settings -> Addons -> Addon Store -> Kebab (3 dots) menu -> Repositories
2. Add this repository: https://github.com/edmundhighcock/hassio-repository
3. Go to Settings -> Addons -> Addon Store -> Kebab (3 dots) menu -> Check for Updates
4. Refresh the page

You should now see the repository which should  include this addon. 

### Install

1. Install the addon.
2. Ensure "Start on Boot" and "Watchdog" are checked.
3. Start the addon.
4. Select "Show in Sidebar" if desired.

