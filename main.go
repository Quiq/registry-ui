package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/CloudyKit/jet"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/quiq/docker-registry-ui/events"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/robfig/cron"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v2"
)

type configData struct {
	ListenAddr            string   `yaml:"listen_addr"`
	BasePath              string   `yaml:"base_path"`
	RegistryURL           string   `yaml:"registry_url"`
	VerifyTLS             bool     `yaml:"verify_tls"`
	Username              string   `yaml:"registry_username"`
	Password              string   `yaml:"registry_password"`
	PasswordFile          string   `yaml:"registry_password_file"`
	EventListenerToken    string   `yaml:"event_listener_token"`
	EventRetentionDays    int      `yaml:"event_retention_days"`
	EventDatabaseDriver   string   `yaml:"event_database_driver"`
	EventDatabaseLocation string   `yaml:"event_database_location"`
	EventDeletionEnabled  bool     `yaml:"event_deletion_enabled"`
	CacheRefreshInterval  uint8    `yaml:"cache_refresh_interval"`
	AnyoneCanDelete       bool     `yaml:"anyone_can_delete"`
	Admins                []string `yaml:"admins"`
	Debug                 bool     `yaml:"debug"`
	PurgeTagsKeepDays     int      `yaml:"purge_tags_keep_days"`
	PurgeTagsKeepCount    int      `yaml:"purge_tags_keep_count"`
	PurgeTagsSchedule     string   `yaml:"purge_tags_schedule"`
}

type template struct {
	View *jet.Set
}

type apiClient struct {
	client        *registry.Client
	eventListener *events.EventListener
	config        configData
}

func main() {
	var (
		a           apiClient
		configFile  string
		purgeTags   bool
		purgeDryRun bool
	)
	flag.StringVar(&configFile, "config-file", "config.yml", "path to the config file")
	flag.BoolVar(&purgeTags, "purge-tags", false, "purge old tags instead of running a web server")
	flag.BoolVar(&purgeDryRun, "dry-run", false, "dry-run for purging task, does not delete anything")
	flag.Parse()

	// Read config file.
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		panic(err)
	}
	bytes, err := ioutil.ReadFile(configFile)
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(bytes, &a.config); err != nil {
		panic(err)
	}
	// Validate registry URL.
	u, err := url.Parse(a.config.RegistryURL)
	if err != nil {
		panic(err)
	}
	// Normalize base path.
	if a.config.BasePath != "" {
		if !strings.HasPrefix(a.config.BasePath, "/") {
			a.config.BasePath = "/" + a.config.BasePath
		}
		if strings.HasSuffix(a.config.BasePath, "/") {
			a.config.BasePath = a.config.BasePath[0 : len(a.config.BasePath)-1]
		}
	}
	// Read password from file.
	if a.config.PasswordFile != "" {
		if _, err := os.Stat(a.config.PasswordFile); os.IsNotExist(err) {
			panic(err)
		}
		passwordBytes, err := ioutil.ReadFile(a.config.PasswordFile)
		if err != nil {
			panic(err)
		}
		a.config.Password = strings.TrimSuffix(string(passwordBytes[:]), "\n")
	}

	// Init registry API client.
	a.client = registry.NewClient(a.config.RegistryURL, a.config.VerifyTLS, a.config.Username, a.config.Password)
	if a.client == nil {
		panic(fmt.Errorf("cannot initialize api client or unsupported auth method"))
	}

	// Execute CLI task and exit.
	if purgeTags {
		a.purgeOldTags(purgeDryRun)
		return
	}
	// Schedules to purge tags.
	if a.config.PurgeTagsSchedule != "" {
		c := cron.New()
		task := func() {
			a.purgeOldTags(purgeDryRun)
		}
		if err := c.AddFunc(a.config.PurgeTagsSchedule, task); err != nil {
			panic(fmt.Errorf("Invalid schedule format: %s", a.config.PurgeTagsSchedule))
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
	e.Renderer = setupRenderer(a.config.Debug, u.Host, a.config.BasePath)

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

func (a *apiClient) viewRepositories(c echo.Context) error {
	namespace := c.Param("namespace")
	if namespace == "" {
		namespace = "library"
	}

	repos, _ := a.client.Repositories(true)[namespace]
	data := jet.VarMap{}
	data.Set("namespace", namespace)
	data.Set("namespaces", a.client.Namespaces())
	data.Set("repos", repos)
	data.Set("tagCounts", a.client.TagCounts())

	return c.Render(http.StatusOK, "repositories.html", data)
}

func (a *apiClient) viewTags(c echo.Context) error {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	repoPath := repo
	if namespace != "library" {
		repoPath = fmt.Sprintf("%s/%s", namespace, repo)
	}

	tags := a.client.Tags(repoPath)
	deleteAllowed := a.checkDeletePermission(c.Request().Header.Get("X-WEBAUTH-USER"))

	data := jet.VarMap{}
	data.Set("namespace", namespace)
	data.Set("repo", repo)
	data.Set("tags", tags)
	data.Set("deleteAllowed", deleteAllowed)
	repoPath, _ = url.PathUnescape(repoPath)
	data.Set("events", a.eventListener.GetEvents(repoPath))

	return c.Render(http.StatusOK, "tags.html", data)
}

func (a *apiClient) viewTagInfo(c echo.Context) error {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	tag := c.Param("tag")
	repoPath := repo
	if namespace != "library" {
		repoPath = fmt.Sprintf("%s/%s", namespace, repo)
	}

	sha256, infoV1, infoV2 := a.client.TagInfo(repoPath, tag, false)
	manifests := a.client.Manifests(repoPath, tag)
	if (infoV1 == "" || infoV2 == "") && len(manifests) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/%s/%s", a.config.BasePath, namespace, repo))
	}
	isListOnly := (infoV1 == "" && infoV2 == "")
	newRepoPath := gjson.Get(infoV1, "name").String()
	if newRepoPath != "" {
		repoPath = newRepoPath
	}

	var imageSize int64
	if gjson.Get(infoV2, "layers").Exists() {
		for _, s := range gjson.Get(infoV2, "layers.#.size").Array() {
			imageSize = imageSize + s.Int()
		}
	} else {
		for _, s := range gjson.Get(infoV2, "history.#.v1Compatibility").Array() {
			imageSize = imageSize + gjson.Get(s.String(), "Size").Int()
		}
	}

	var layersV2 []map[string]gjson.Result
	for _, s := range gjson.Get(infoV2, "layers").Array() {
		layersV2 = append(layersV2, s.Map())
	}

	var layersV1 []map[string]interface{}
	for _, s := range gjson.Get(infoV1, "history.#.v1Compatibility").Array() {
		m, _ := gjson.Parse(s.String()).Value().(map[string]interface{})
		// Sort key in the map to show the ordered on UI.
		m["ordered_keys"] = registry.SortedMapKeys(m)
		layersV1 = append(layersV1, m)
	}

	layersCount := len(layersV2)
	if layersCount == 0 {
		layersCount = len(gjson.Get(infoV1, "fsLayers").Array())
	}

	isDigest := strings.HasPrefix(tag, "sha256:")
	var digests []map[string]interface{}
	for _, s := range manifests {
		r, _ := gjson.Parse(s.String()).Value().(map[string]interface{})
		if s.Get("mediaType").String() == "application/vnd.docker.distribution.manifest.v2+json" {
			_, _, dInfo := a.client.TagInfo(repoPath, s.Get("digest").String(), false)
			var dSize int64
			for _, d := range gjson.Get(dInfo, "layers.#.size").Array() {
				dSize = dSize + d.Int()
			}
			r["size"] = dSize
		} else {
			r["size"] = s.Get("size").Int()
		}
		r["ordered_keys"] = registry.SortedMapKeys(r)
		digests = append(digests, r)
	}

	data := jet.VarMap{}
	data.Set("namespace", namespace)
	data.Set("repo", repo)
	data.Set("sha256", sha256)
	data.Set("imageSize", imageSize)
	data.Set("tag", tag)
	data.Set("repoPath", repoPath)
	data.Set("created", gjson.Get(gjson.Get(infoV1, "history.0.v1Compatibility").String(), "created").String())
	data.Set("layersCount", layersCount)
	data.Set("layersV2", layersV2)
	data.Set("layersV1", layersV1)
	data.Set("isDigest", isDigest)
	data.Set("isListOnly", isListOnly)
	data.Set("digests", digests)

	return c.Render(http.StatusOK, "tag_info.html", data)
}

func (a *apiClient) deleteTag(c echo.Context) error {
	namespace := c.Param("namespace")
	repo := c.Param("repo")
	tag := c.Param("tag")
	repoPath := repo
	if namespace != "library" {
		repoPath = fmt.Sprintf("%s/%s", namespace, repo)
	}

	if a.checkDeletePermission(c.Request().Header.Get("X-WEBAUTH-USER")) {
		a.client.DeleteTag(repoPath, tag)
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/%s/%s", a.config.BasePath, namespace, repo))
}

// checkDeletePermission check if tag deletion is allowed whether by anyone or permitted users.
func (a *apiClient) checkDeletePermission(user string) bool {
	deleteAllowed := a.config.AnyoneCanDelete
	if !deleteAllowed {
		for _, u := range a.config.Admins {
			if u == user {
				deleteAllowed = true
				break
			}
		}
	}
	return deleteAllowed
}

// viewLog view events from sqlite.
func (a *apiClient) viewLog(c echo.Context) error {
	data := jet.VarMap{}
	data.Set("events", a.eventListener.GetEvents(""))

	return c.Render(http.StatusOK, "event_log.html", data)
}

// receiveEvents receive events.
func (a *apiClient) receiveEvents(c echo.Context) error {
	a.eventListener.ProcessEvents(c.Request())
	return c.String(http.StatusOK, "OK")
}

// purgeOldTags purges old tags.
func (a *apiClient) purgeOldTags(dryRun bool) {
	registry.PurgeOldTags(a.client, dryRun, a.config.PurgeTagsKeepDays, a.config.PurgeTagsKeepCount)
}
