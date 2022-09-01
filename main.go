package main

import (
	"flag"
	"fmt"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/quiq/docker-registry-ui/events"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/robfig/cron"
	"github.com/sirupsen/logrus"
)

type apiClient struct {
	client        *registry.Client
	eventListener *events.EventListener
	config        *configData
}

func main() {
	var (
		a apiClient

		configFile, loggingLevel string
		purgeTags, purgeDryRun   bool
	)
	flag.StringVar(&configFile, "config-file", "config.yml", "path to the config file")
	flag.StringVar(&loggingLevel, "log-level", "info", "logging level")
	flag.BoolVar(&purgeTags, "purge-tags", false, "purge old tags instead of running a web server")
	flag.BoolVar(&purgeDryRun, "dry-run", false, "dry-run for purging task, does not delete anything")
	flag.Parse()

	// Setup logging
	if loggingLevel != "info" {
		if level, err := logrus.ParseLevel(loggingLevel); err == nil {
			logrus.SetLevel(level)
		}
	}

	// Read config file
	a.config = readConfig(configFile)
	a.config.PurgeConfig.DryRun = purgeDryRun

	// Init registry API client.
	a.client = registry.NewClient(a.config.RegistryURL, a.config.VerifyTLS, a.config.Username, a.config.Password)
	if a.client == nil {
		panic(fmt.Errorf("cannot initialize api client or unsupported auth method"))
	}

	purgeFunc := func() {
		registry.PurgeOldTags(a.client, a.config.PurgeConfig)
	}

	// Execute CLI task and exit.
	if purgeTags {
		purgeFunc()
		return
	}

	// Schedules to purge tags.
	if a.config.PurgeTagsSchedule != "" {
		c := cron.New()
		if err := c.AddFunc(a.config.PurgeTagsSchedule, purgeFunc); err != nil {
			panic(fmt.Errorf("invalid schedule format: %s", a.config.PurgeTagsSchedule))
		}
		c.Start()
	}

	// Count tags in background.
	go a.client.CountTags(a.config.CacheRefreshInterval)

	if a.config.EventDatabaseDriver != "sqlite3" && a.config.EventDatabaseDriver != "mysql" {
		panic(fmt.Errorf("event_database_driver should be either sqlite3 or mysql"))
	}
	a.eventListener = events.NewEventListener(
		a.config.EventDatabaseDriver, a.config.EventDatabaseLocation, a.config.EventRetentionDays, a.config.EventDeletionEnabled,
	)

	// Template engine init.
	e := echo.New()
	registryHost, _ := url.Parse(a.config.RegistryURL) // validated already in config.go
	e.Renderer = setupRenderer(a.config.Debug, registryHost.Host, a.config.BasePath)

	// Web routes.
	e.File("/favicon.ico", "static/favicon.ico")
	e.Static(a.config.BasePath+"/static", "static")
	if a.config.BasePath != "" {
		e.GET(a.config.BasePath, a.viewRepositories)
	}
	e.GET(a.config.BasePath+"/", a.viewRepositories)
	e.GET(a.config.BasePath+"/:namespace", a.viewRepositories)
	e.GET(a.config.BasePath+"/:namespace/:repo", a.viewTags)
	e.GET(a.config.BasePath+"/:namespace/:repo/:tag", a.viewTagInfo)
	e.GET(a.config.BasePath+"/:namespace/:repo/:tag/delete", a.deleteTag)
	e.GET(a.config.BasePath+"/events", a.viewLog)

	// Protected event listener.
	p := e.Group(a.config.BasePath + "/api")
	p.Use(middleware.KeyAuthWithConfig(middleware.KeyAuthConfig{
		Validator: middleware.KeyAuthValidator(func(token string, c echo.Context) (bool, error) {
			return token == a.config.EventListenerToken, nil
		}),
	}))
	p.POST("/events", a.receiveEvents)

	e.Logger.Fatal(e.Start(a.config.ListenAddr))
}
