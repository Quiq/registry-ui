## Registry UI

[![Go Report Card](https://goreportcard.com/badge/github.com/quiq/registry-ui)](https://goreportcard.com/report/github.com/quiq/registry-ui)

### Overview

* Web UI for Docker Registry or similar alternatives
* Fast, simple and small package
* Browse catalog of repositories and tags
* Show an arbitrary level of repository tree
* Support Docker and OCI image formats
* Support image and image index manifests (multi-platform images)
* Display full information about image index and links to the underlying sub-images
* Display full information about image, its layers and config file (command history)
* Event listener for notification events coming from Registry
* Store events in Sqlite or MySQL database
* CLI option to maintain the tag retention: purge tags older than X days keeping at least Y tags etc.
* Automatically discover an authentication method: basic auth, token service, keychain etc.
* The list of repositories and tag counts are cached and refreshed in background

No TLS or authentication is implemented on the UI instance itself.
Assuming you will put it behind nginx, oauth2_proxy or similar.

Docker images [quiq/registry-ui](https://hub.docker.com/r/quiq/registry-ui/tags/)

### Quick start

Run a Docker registry in your host (if you don't already had one):

    docker run -d --network host \
        --name registry registry:2

Run registry UI directly connected to it:

    docker run -d --network host \
        -e REGISTRY_HOSTNAME=127.0.0.1:5000 \
        -e REGISTRY_INSECURE=true \
        --name registry-ui quiq/registry-ui

Push any Docker image to 127.0.0.1:5000/owner/name and go into http://127.0.0.1:8000 with
your web browser.

### Configuration

The configuration is stored in `config.yml` and the options are self-descriptive.

You can override any config option via environment variables using SECTION_KEY_NAME syntax,
e.g. `LISTEN_ADDR`, `PERFORMANCE_TAGS_COUNT_REFRESH_INTERVAL`, `REGISTRY_HOSTNAME` etc.

Passing the full config file through:

    docker run -d -p 8000:8000 -v /local/config.yml:/opt/config.yml:ro quiq/registry-ui

To run with your own root CA certificate, add to the command:

    -v /local/rootcacerts.crt:/etc/ssl/certs/ca-certificates.crt:ro

To preserve sqlite db file with event data, add to the command:

    -v /local/data:/opt/data

Ensure /local/data is owner by nobody (alpine user id is 65534).

You can also run the container with `--read-only` option, however when using using event listener functionality
you need to ensure the sqlite db can be written, i.e. mount a folder as listed above (rw mode).

To run with a custom TZ:

    -e TZ=America/Los_Angeles

## Configure event listener on Docker Registry

To receive events you need to configure Registry as follow:

    notifications:
      endpoints:
        - name: registry-ui
          url: http://registry-ui.local:8000/event-receiver
          headers:
            Authorization: [Bearer abcdefghijklmnopqrstuvwxyz1234567890]
          timeout: 1s
          threshold: 5
          backoff: 10s
          ignoredmediatypes:
            - application/octet-stream

Adjust url and token as appropriate.
If you are running UI with non-default base path, e.g. /ui, the URL path for above will be `/ui/event-receiver` etc.

## Using MySQL instead of sqlite3 for event listener

To use MySQL as a storage you need to change `event_database_driver` and `event_database_location`
settings in the config file. It is expected you create a database mentioned in the location DSN.
Minimal privileges are `SELECT`, `INSERT`, `DELETE`.
You can create a table manually if you don't want to grant `CREATE` permission:

	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTO_INCREMENT,
		action CHAR(4) NULL,
		repository VARCHAR(100) NULL,
		tag VARCHAR(100) NULL,
		ip VARCHAR(45) NULL,
		user VARCHAR(50) NULL,
		created DATETIME NULL
	);

### Schedule a cron task for purging tags

To delete tags you need to enable the corresponding option in Docker Registry config. For example:

    storage:
      delete:
        enabled: true

The following example shows how to run a cron task to purge tags older than X days but also keep
at least Y tags no matter how old. Assuming container has been already running.

    10 3 * * * root docker exec -t registry-ui /opt/registry-ui -purge-tags

You can try to run in dry-run mode first to see what is going to be purged:

    docker exec -t registry-ui /opt/registry-ui -purge-tags -dry-run

### Screenshots

Repository list:

![image](screenshots/1.png)

Tag list:

![image](screenshots/2.png)

Image Index info:

![image](screenshots/3.png)

Image info:

![image](screenshots/4.png)
