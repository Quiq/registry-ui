package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/quiq/registry-ui/events"
	"github.com/quiq/registry-ui/registry"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type apiClient struct {
	client        *registry.Client
	eventListener *events.EventListener
}

func main() {
	var (
		a apiClient

		configFile, loggingLevel string
		purgeFromRepos           string
		purgeTags, purgeDryRun   bool
	)
	flag.StringVar(&configFile, "config-file", "config.yml", "path to the config file")
	flag.StringVar(&loggingLevel, "log-level", "info", "logging level")

	flag.BoolVar(&purgeTags, "purge-tags", false, "purge old tags instead of running a web server")
	flag.BoolVar(&purgeDryRun, "dry-run", false, "dry-run for purging task, does not delete anything")
	flag.StringVar(&purgeFromRepos, "purge-from-repos", "", "comma-separated list of repos to purge instead of all")
	flag.Parse()

	// Setup logging
	if loggingLevel != "info" {
		if level, err := logrus.ParseLevel(loggingLevel); err == nil {
			logrus.SetLevel(level)
		}
	}

	// Read config file
	viper.SetConfigName(strings.Split(filepath.Base(configFile), ".")[0])
	viper.AddConfigPath(filepath.Dir(configFile))
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error reading config file: %w", err))
	}

	// Init registry API client.
	a.client = registry.NewClient()

	// Execute CLI task and exit.
	if purgeTags {
		registry.PurgeOldTags(a.client, purgeDryRun, purgeFromRepos)
		return
	}

	go a.client.StartBackgroundJobs()
	a.eventListener = events.NewEventListener()

	// Template engine init.
	e := echo.New()
	// e.Use(middleware.Logger())
	e.Use(loggingMiddleware())
	e.Use(recoverMiddleware())

	basePath := viper.GetString("uri_base_path")
	// Normalize base path.
	basePath = strings.Trim(basePath, "/")
	if basePath != "" {
		basePath = "/" + basePath
	}
	e.Renderer = setupRenderer(basePath)

	// Web routes.
	e.File("/favicon.ico", "static/favicon.ico")
	e.Static(basePath+"/static", "static")

	p := e.Group(basePath)
	if basePath != "" {
		e.GET(basePath, a.viewCatalog)
	}
	p.GET("/", a.viewCatalog)
	p.GET("/:repoPath", a.viewCatalog)
	p.GET("/event-log", a.viewEventLog)
	p.GET("/delete-tag", a.deleteTag)

	// Protected event listener.
	pp := e.Group("/event-receiver")
	pp.Use(middleware.KeyAuthWithConfig(middleware.KeyAuthConfig{
		Validator: middleware.KeyAuthValidator(func(token string, c echo.Context) (bool, error) {
			return token == viper.GetString("event_listener.bearer_token"), nil
		}),
	}))
	pp.POST("", a.receiveEvents)

	e.Logger.Fatal(e.Start(viper.GetString("listen_addr")))
}
