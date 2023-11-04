# hassio-repository
A repository for Home Assistant Addons

[Home Assistant](https://www.home-assistant.io/) is a smart home operating system that allows you to control and automate your house. It can be extended via community addons to carry out many different tasks, allowing you to automate and accelerate all parts of your work and home tasks.

This repository is a collection of add-ons which have been created to fill current holes in the community collection.

## Adding this Repository

To add this repository, you need to be running Home Assistant as an operating system, either in a VM or standalone (i.e. not in a container). 

1. Copy the URL of this github repo (https, not ssh). 
2. Go to Settings -> Add-ons -> Add-on Store, then click the 3-dots (kebab) menu and select repositories. 
3. Add the repo by pasting the URL and clicking "Add".
4. Then select "Check for Updates" from the same 3-dots menu.
5. Then refresh the page.

## Add-ons In this Repository

## Fava + Beancount Personal Accounting Software

Beancount is well-established python tool for carrying out double-entry accounting.

### [Taiga Addon](https://github.com/edmundhighcock/hassio-repository/tree/main/taiga)

[Taiga](https://www.taiga.io/) is an open-source agile project manager which is great for managing both your personal and work activities.

#### Addon for RabbitMQ (Supplemental)

This addon provides a rabbitmq message broker. At the moment it is hard-wired to support the Taiga addon and doesn't have the ability to customise e.g. the username or virtual host.


#### Postgres (Supplemental)

This addon provides a postgresql database server. Like the RabbitMQ add-on, at the moment it is hard-wired to support the Taiga addon and doesn't have the ability to customise e.g. the username, database etc. 
