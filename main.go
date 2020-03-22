package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/CloudyKit/jet"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/quiq/docker-registry-ui/events"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/robfig/cron"
	"github.com/sirupsen/logrus"
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

type breadCrumb struct {
	segment string
	path    string
}

func getBreadCrumbs(path string) []breadCrumb {
	ret := []breadCrumb{}
	segments := strings.Split(path, "/")
	for i := 0; i < len(segments); i++ {
		e := breadCrumb{segment: segments[i], path: strings.Join(segments[0:i+1], "/")}
		ret = append(ret, e)
	}
	return ret
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

	if loggingLevel != "info" {
		if level, err := logrus.ParseLevel(loggingLevel); err == nil {
			logrus.SetLevel(level)
		}
	}

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
		e.GET(a.config.BasePath, a.dispatchRequest)
	}
	e.GET(a.config.BasePath+"/", a.dispatchRequest)
	e.GET(a.config.BasePath+"/*", a.dispatchRequest)
	e.GET(a.config.BasePath+"/events", a.viewLog)
	e.GET(a.config.BasePath+"/invalidate_cache", a.invalidateCache)

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

func (a *apiClient) dispatchRequest(c echo.Context) error {
	path := c.Request().URL.Path
	if strings.HasPrefix(path, a.config.BasePath) {
		path = path[len(a.config.BasePath):]
	}
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	segments := strings.Split(path, "/")
	manifestRequest, _ := regexp.MatchString(".*/manifests/[^/]*$", path)
	manifestDeleteRequest, _ := regexp.MatchString(".*/manifests/[^/]*/delete$", path)
	if manifestRequest {
		repoPath := strings.Join(segments[0:len(segments)-2], "/")
		tagName := segments[len(segments)-1]
		return a.viewTagInfo(c, repoPath, tagName)
	} else if manifestDeleteRequest {
		repoPath := strings.Join(segments[0:len(segments)-3], "/")
		tagName := segments[len(segments)-2]
		return a.deleteTag(c, repoPath, tagName)
	} else {
		return a.listRepo(c, path)
	}
}

func (a *apiClient) invalidateCache(c echo.Context) error {
	a.client.InvalidateCache()
	return c.Redirect(http.StatusSeeOther, a.config.BasePath+"/")
}

func (a *apiClient) listRepo(c echo.Context, repoPath string) error {
	tags := a.client.Tags(repoPath)
	repos := a.client.RepositoriesList(true)

	filterExpression := repoPath
	if !strings.HasSuffix(repoPath, "/") && repoPath != "" {
		filterExpression += "/"
	}
	matching_repos := registry.FilterStringSlice(repos, func(s string) bool {
		return strings.HasPrefix(s, filterExpression) && len(s) > len(repoPath)
	})
	deleteAllowed := a.checkDeletePermission(c.Request().Header.Get("X-WEBAUTH-USER"))

	data := jet.VarMap{}
	data.Set("repoPath", repoPath)
	data.Set("breadCrumbs", getBreadCrumbs(repoPath))
	data.Set("tags", tags)
	data.Set("repos", matching_repos)
	data.Set("deleteAllowed", deleteAllowed)
	data.Set("tagCounts", a.client.TagCounts())
	repoPath, _ = url.PathUnescape(repoPath)
	data.Set("events", a.eventListener.GetEvents(repoPath))

	return c.Render(http.StatusOK, "list.html", data)
}

func (a *apiClient) viewTagInfo(c echo.Context, repoPath string, tag string) error {
	repo := repoPath

	// Retrieve full image info from various versions of manifests
	sha256, infoV1, infoV2 := a.client.TagInfo(repoPath, tag, false)
	sha256list, manifests := a.client.ManifestList(repoPath, tag)
	if (infoV1 == "" || infoV2 == "") && len(manifests) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/%s", a.config.BasePath, repo))
	}

	created := gjson.Get(gjson.Get(infoV1, "history.0.v1Compatibility").String(), "created").String()
	isDigest := strings.HasPrefix(tag, "sha256:")
	if len(manifests) > 0 {
		sha256 = sha256list
	}

	// Gather layers v2
	var layersV2 []map[string]gjson.Result
	for _, s := range gjson.Get(infoV2, "layers").Array() {
		layersV2 = append(layersV2, s.Map())
	}

	// Gather layers v1
	var layersV1 []map[string]interface{}
	for _, s := range gjson.Get(infoV1, "history.#.v1Compatibility").Array() {
		m, _ := gjson.Parse(s.String()).Value().(map[string]interface{})
		// Sort key in the map to show the ordered on UI.
		m["ordered_keys"] = registry.SortedMapKeys(m)
		layersV1 = append(layersV1, m)
	}

	// Count image size
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

	// Count layers
	layersCount := len(layersV2)
	if layersCount == 0 {
		layersCount = len(gjson.Get(infoV1, "fsLayers").Array())
	}

	// Gather sub-image info of multi-arch or cache image
	var digestList []map[string]interface{}
	for _, s := range manifests {
		r, _ := gjson.Parse(s.String()).Value().(map[string]interface{})
		if s.Get("mediaType").String() == "application/vnd.docker.distribution.manifest.v2+json" {
			// Sub-image of the specific arch.
			_, dInfoV1, _ := a.client.TagInfo(repoPath, s.Get("digest").String(), true)
			var dSize int64
			for _, d := range gjson.Get(dInfoV1, "layers.#.size").Array() {
				dSize = dSize + d.Int()
			}
			r["size"] = dSize
			// Create link here because there is a bug with jet template when referencing a value by map key in the "if" condition under "range".
			if r["mediaType"] == "application/vnd.docker.distribution.manifest.v2+json" {
				r["digest"] = fmt.Sprintf(`<a href="%s/%s/%s">%s</a>`, a.config.BasePath, repo, r["digest"], r["digest"])
			}
		} else {
			// Sub-image of the cache type.
			r["size"] = s.Get("size").Int()
		}
		r["ordered_keys"] = registry.SortedMapKeys(r)
		digestList = append(digestList, r)
	}

	// Populate template vars
	data := jet.VarMap{}
	data.Set("repo", repo)
	data.Set("tag", tag)
	data.Set("repoPath", repoPath)
	data.Set("breadCrumbs", getBreadCrumbs(repoPath))
	data.Set("sha256", sha256)
	data.Set("imageSize", imageSize)
	data.Set("created", created)
	data.Set("layersCount", layersCount)
	data.Set("layersV2", layersV2)
	data.Set("layersV1", layersV1)
	data.Set("isDigest", isDigest)
	data.Set("digestList", digestList)

	return c.Render(http.StatusOK, "tag_info.html", data)
}

func (a *apiClient) deleteTag(c echo.Context, repoPath string, tag string) error {
	if a.checkDeletePermission(c.Request().Header.Get("X-WEBAUTH-USER")) {
		a.client.DeleteTag(repoPath, tag)
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/%s", a.config.BasePath, repoPath))
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
