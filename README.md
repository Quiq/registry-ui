## Docker Registry UI

[![Go Report Card](https://goreportcard.com/badge/github.com/quiq/docker-registry-ui)](https://goreportcard.com/report/github.com/quiq/docker-registry-ui)

### Overview

* Web UI for Docker Registry
* Browse namespaces, repositories and tags
* Display image details by layers
* Display sub-images of multi-arch or cache type of image
* Support Manifest v2 schema 1, Manifest v2 schema 2, Manifest List v2 schema 2 and their confusing combinations
* Fast and small, written on Go
* Automatically discover an authentication method (basic auth, token service etc.)
* Caching the list of repositories, tag counts and refreshing in background
* Event listener of notification events coming from Registry
* Store events in sqlite or MySQL database
* CLI option to maintain the tags retention: purge tags older than X days keeping at least Y tags

No TLS or authentication implemented on the UI web server itself.
Assuming you will proxy it behind nginx, oauth2_proxy or something.

Docker images [quiq/docker-registry-ui](https://hub.docker.com/r/quiq/docker-registry-ui/tags/)

### Configuration

The configuration is stored in `config.yml` and the options are self-descriptive.

### Run UI

    docker run -d -p 8000:8000 -v /local/config.yml:/opt/config.yml:ro \
        --name=registry-ui quiq/docker-registry-ui

To run with your own root CA certificate, add to the command:

    -v /local/rootcacerts.crt:/etc/ssl/certs/ca-certificates.crt:ro

To preserve sqlite db file with event notifications data, add to the command:

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
        - name: docker-registry-ui
          url: http://docker-registry-ui.local:8000/api/events
          headers:
            Authorization: [Bearer abcdefghijklmnopqrstuvwxyz1234567890]
          timeout: 1s
          threshold: 5
          backoff: 10s
          ignoredmediatypes:
            - application/octet-stream

Adjust url and token as appropriate.
If you are running UI from non-root base path, e.g. /ui, the URL path for above will be `/ui/api/events`.

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
		ip VARCHAR(15) NULL,
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

    10 3 * * * root docker exec -t registry-ui /opt/docker-registry-ui -purge-tags

You can try to run in dry-run mode first to see what is going to be purged:

    docker exec -t registry-ui /opt/docker-registry-ui -purge-tags -dry-run

Alternatively, you can schedule the purging task with built-in cron feature:

    purge_tags_schedule: '0 10 3 * * *'

Note, the cron schedule format includes seconds! See https://godoc.org/github.com/robfig/cron

### Debug mode

To increase http request verbosity, run container with `-e GOREQUEST_DEBUG=1`.

### About Docker image formats...

Docker image formats and their confusing combinations as supported by this UI:

* Manifest v2 schema 1 only: older format, e.g. created with Docker 1.9.
* Manifest v2 schema 1 + Manifest v2 schema 2: current format of a single image, the image history are coming from schema 1, should be referenced by repo:tag name.
* Manifest v2 schema 1 + Manifest List v2 schema 2: multi-arch image format containing digests of sub-images, the image history are coming from schema 1 (no idea from what sub-image it was picked up when created), should be referenced by repo:tag name.
* Manifest v2 schema 2: current image format referenced by its digest sha256, no image history.
* Manifest List v2 schema 2: multi-arch image referenced by its digest sha256 or cache image referenced by tag name, no image history.

### Screenshots

Repository list / home page:

![image](screenshots/1.png)

Repository tag list:

![image](screenshots/2.png)

Tag info page:

![image](screenshots/3.png)

Event log page:

![image](screenshots/4.png)
