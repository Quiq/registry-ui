package registry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/grafana/grafana/pkg/cmd/grafana-cli/logger"
	// 🐒 patching of "database/sql".
	_ "github.com/mattn/go-sqlite3"
	"github.com/tidwall/gjson"
)

const (
	dbFile = "data/registry_events.db"
	schema = `
	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action CHAR(4) NULL,
		repository VARCHAR(100) NULL,
		tag VARCHAR(100) NULL,
		ip VARCHAR(15) NULL,
		user VARCHAR(50) NULL,
		created DATETIME NULL
	);
`
)

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
	Created    time.Time
}

// ProcessEvents parse and store registry events
func ProcessEvents(request *http.Request, retention int) {
	logger := setupLogging("registry.event_listener")
	decoder := json.NewDecoder(request.Body)
	var t eventData
	if err := decoder.Decode(&t); err != nil {
		logger.Errorf("Problem decoding event from request: %+v", request)
		return
	}
	logger.Debugf("Received event: %+v", t)
	j, _ := json.Marshal(t)

	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		logger.Error("Error opening sqlite db: ", err)
		return
	}
	defer db.Close()

	_, err = db.Exec(schema)
	if err != nil {
		logger.Error("Error creating a table: ", err)
		return
	}

	stmt, _ := db.Prepare("INSERT INTO events(action, repository, tag, ip, user, created) values(?,?,?,?,?,DateTime('now'))")
	for _, e := range gjson.GetBytes(j, "events").Array() {
		// Ignore calls by docker-registry-ui itself.
		if e.Get("request.useragent").String() == "docker-registry-ui" {
			continue
		}
		action := e.Get("action").String()
		repository := e.Get("target.repository").String()
		tag := e.Get("target.tag").String()
		// Tag is empty in case of signed pull.
		if tag == "" {
			tag = e.Get("target.digest").String()
		}
		ip := strings.Split(e.Get("request.addr").String(), ":")[0]
		user := e.Get("actor.name").String()
		logger.Debugf("Parsed event data: %s %s:%s %s %s ", action, repository, tag, ip, user)

		res, err := stmt.Exec(action, repository, tag, ip, user)
		if err != nil {
			logger.Error("Error inserting a row: ", err)
			return
		}
		id, _ := res.LastInsertId()
		logger.Debug("New event added with id ", id)
	}

	// Purge old records.
	stmt, _ = db.Prepare("DELETE FROM events WHERE created < DateTime('now',?)")
	res, _ := stmt.Exec(fmt.Sprintf("-%d day", retention))
	count, _ := res.RowsAffected()
	logger.Debug("Rows deleted: ", count)
}

// GetEvents retrieve events from sqlite db
func GetEvents() []EventRow {
	var events []EventRow
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		logger.Error("Error opening sqlite db: ", err)
		return events
	}
	defer db.Close()

	rows, err := db.Query("SELECT * FROM events ORDER BY id DESC LIMIT 1000")
	if err != nil {
		logger.Error("Error selecting from table: ", err)
		return events
	}

	for rows.Next() {
		var row EventRow
		rows.Scan(&row.ID, &row.Action, &row.Repository, &row.Tag, &row.IP, &row.User, &row.Created)
		events = append(events, row)
	}
	rows.Close()
	return events
}
