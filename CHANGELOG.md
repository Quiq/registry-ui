## Changelog

### Unreleased

* When using MySQL for event storage, do not leak connections.
* Last events were not shown when viewing a repo of non-default namespace.

### 0.6

* Add MySQL along with sqlite3 support as a registry events storage.
  New config settings `event_database_driver`, `event_database_location`.
* Bump Go version and dependencies.

### 0.5

* Initial public version.
