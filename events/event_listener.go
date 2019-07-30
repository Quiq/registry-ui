package events

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hhkbp2/go-logging"
	"github.com/quiq/docker-registry-ui/registry"

	// 🐒 patching of "database/sql".
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/mattn/go-sqlite3"
	"github.com/tidwall/gjson"
)

const (
	schemaSQLite = `
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action CHAR(5) NULL,
		repository VARCHAR(100) NULL,
		tag VARCHAR(100) NULL,
		ip VARCHAR(15) NULL,
		user VARCHAR(50) NULL,
		created DATETIME NULL
	);
`
)

// EventListener event listener
type EventListener struct {
	databaseDriver   string
	databaseLocation string
	retention        int
	eventDeletion    bool
	logger           logging.Logger
}

type eventData struct {
	Events []interface{} `json:"events"`
}

// EventRow event row from sqlite
type EventRow struct {
	ID         int
	Action     string
	Repository string
	Tag        string
	IP         string
	User       string
	Created    string
}

// NewEventListener initialize EventListener.
func NewEventListener(databaseDriver, databaseLocation string, retention int, eventDeletion bool) *EventListener {
	return &EventListener{
		databaseDriver:   databaseDriver,
		databaseLocation: databaseLocation,
		retention:        retention,
		eventDeletion:    eventDeletion,
		logger:           registry.SetupLogging("events.event_listener"),
	}
}

// ProcessEvents parse and store registry events
func (e *EventListener) ProcessEvents(request *http.Request) {
	decoder := json.NewDecoder(request.Body)
	var t eventData
	if err := decoder.Decode(&t); err != nil {
		e.logger.Errorf("Problem decoding event from request: %+v", request)
		return
	}
	e.logger.Debugf("Received event: %+v", t)
	j, _ := json.Marshal(t)

	db, err := e.getDatabaseHandler()
	if err != nil {
		e.logger.Error(err)
		return
	}
	defer db.Close()

	now := "DateTime('now')"
	if e.databaseDriver == "mysql" {
		now = "NOW()"
	}
	stmt, _ := db.Prepare("INSERT INTO events(action, repository, tag, ip, user, created) values(?,?,?,?,?," + now + ")")
	for _, i := range gjson.GetBytes(j, "events").Array() {
		// Ignore calls by docker-registry-ui itself.
		if i.Get("request.useragent").String() == "docker-registry-ui" {
			continue
		}
		action := i.Get("action").String()
		repository := i.Get("target.repository").String()
		tag := i.Get("target.tag").String()
		// Tag is empty in case of signed pull.
		if tag == "" {
			tag = i.Get("target.digest").String()
		}
		ip := strings.Split(i.Get("request.addr").String(), ":")[0]
		user := i.Get("actor.name").String()
		e.logger.Debugf("Parsed event data: %s %s:%s %s %s ", action, repository, tag, ip, user)

		res, err := stmt.Exec(action, repository, tag, ip, user)
		if err != nil {
			e.logger.Error("Error inserting a row: ", err)
			return
		}
		id, _ := res.LastInsertId()
		e.logger.Debug("New event added with id ", id)
	}

	// Purge old records.
	if !e.eventDeletion {
		return
	}
	var res sql.Result
	if e.databaseDriver == "mysql" {
		stmt, _ := db.Prepare("DELETE FROM events WHERE created < DATE_SUB(NOW(), INTERVAL ? DAY)")
		res, _ = stmt.Exec(e.retention)
	} else {
		stmt, _ := db.Prepare("DELETE FROM events WHERE created < DateTime('now',?)")
		res, _ = stmt.Exec(fmt.Sprintf("-%d day", e.retention))
	}
	count, _ := res.RowsAffected()
	e.logger.Debug("Rows deleted: ", count)
}

// GetEvents retrieve events from sqlite db
func (e *EventListener) GetEvents(repository string) []EventRow {
	var events []EventRow

	db, err := e.getDatabaseHandler()
	if err != nil {
		e.logger.Error(err)
		return events
	}
	defer db.Close()

	query := "SELECT * FROM events ORDER BY id DESC LIMIT 1000"
	if repository != "" {
		query = fmt.Sprintf("SELECT * FROM events WHERE repository='%s' ORDER BY id DESC LIMIT 5", repository)
	}
	rows, err := db.Query(query)
	if err != nil {
		e.logger.Error("Error selecting from table: ", err)
		return events
	}
	defer rows.Close()

	for rows.Next() {
		var row EventRow
		rows.Scan(&row.ID, &row.Action, &row.Repository, &row.Tag, &row.IP, &row.User, &row.Created)
		events = append(events, row)
	}
	return events
}

func (e *EventListener) getDatabaseHandler() (*sql.DB, error) {
	firstRun := false
	schema := schemaSQLite
	if e.databaseDriver == "sqlite3" {
		if _, err := os.Stat(e.databaseLocation); os.IsNotExist(err) {
			firstRun = true
		}
	}

	// Open db connection.
	db, err := sql.Open(e.databaseDriver, e.databaseLocation)
	if err != nil {
		return nil, fmt.Errorf("Error opening %s db: %s", e.databaseDriver, err)
	}

	if e.databaseDriver == "mysql" {
		schema = strings.Replace(schema, "AUTOINCREMENT", "AUTO_INCREMENT", 1)
		rows, err := db.Query("SELECT * FROM events LIMIT 1")
		if err != nil {
			firstRun = true
		}
		if rows != nil {
			rows.Close()
		}
	}

	// Create table on first run.
	if firstRun {
		if _, err = db.Exec(schema); err != nil {
			return nil, fmt.Errorf("Error creating a table: %s", err)
		}
	}
	return db, nil
}
