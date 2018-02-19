package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/CloudyKit/jet"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v2"
)

type configData struct {
	ListenAddr           string   `yaml:"listen_addr"`
	RegistryURL          string   `yaml:"registry_url"`
	VerifyTLS            bool     `yaml:"verify_tls"`
	Username             string   `yaml:"registry_username"`
	Password             string   `yaml:"registry_password"`
	EventListenerToken   string   `yaml:"event_listener_token"`
	EventRetentionDays   int      `yaml:"event_retention_days"`
	CacheRefreshInterval uint8    `yaml:"cache_refresh_interval"`
	AnyoneCanDelete      bool     `yaml:"anyone_can_delete"`
	Admins               []string `yaml:"admins"`
	Debug                bool     `yaml:"debug"`
	PurgeTagsKeepDays    int      `yaml:"purge_tags_keep_days"`
	PurgeTagsKeepCount   int      `yaml:"purge_tags_keep_count"`
}

type template struct {
	View *jet.Set
}

type apiClient struct {
	client *registry.Client
	config configData
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

	// Init registry API client.
	a.client = registry.NewClient(a.config.RegistryURL, a.config.VerifyTLS, a.config.Username, a.config.Password)
	if a.client == nil {
		panic(fmt.Errorf("cannot initialize api client or unsupported auth method"))
	}

	// Execute CLI task and exit.
	if purgeTags {
		registry.PurgeOldTags(a.client, purgeDryRun, a.config.PurgeTagsKeepDays, a.config.PurgeTagsKeepCount)
		return
	}

	// Count tags in background.
	go a.client.CountTags(a.config.CacheRefreshInterval)

	// Template engine init.
	view := jet.NewHTMLSet("templates")
	view.SetDevelopmentMode(a.config.Debug)
	view.AddGlobal("registryHost", u.Host)
	view.AddGlobal("pretty_size", func(size interface{}) string {
		var value float64
		switch i := size.(type) {
		case gjson.Result:
			value = float64(i.Int())
		case int64:
			value = float64(i)
		}
		return registry.PrettySize(value)
	})
	view.AddGlobal("pretty_time", func(datetime interface{}) string {
		return strings.Split(strings.Replace(datetime.(string), "T", " ", 1), ".")[0]
	})
	view.AddGlobal("parse_map", func(m interface{}) string {
		var res string
		for _, k := range registry.SortedMapKeys(m) {
			res = res + fmt.Sprintf(`<tr><td style="padding: 0 10px; width: 20%%">%s</td><td style="padding: 0 10px">%v</td></tr>`, k, m.(map[string]interface{})[k])
		}
		return res
	})
	e := echo.New()
	e.Renderer = &template{View: view}

	// Web routes.
	e.Static("/static", "static")
	e.GET("/", a.viewRepositories)
	e.GET("/:namespace", a.viewRepositories)
	e.GET("/:namespace/:repo", a.viewTags)
	e.GET("/:namespace/:repo/:tag", a.viewTagInfo)
	e.GET("/:namespace/:repo/:tag/delete", a.deleteTag)
	e.GET("/events", a.viewLog)

	// Protected event listener.
	p := e.Group("/api")
	p.Use(middleware.KeyAuthWithConfig(middleware.KeyAuthConfig{
		Validator: middleware.KeyAuthValidator(func(token string, c echo.Context) (bool, error) {
			return token == a.config.EventListenerToken, nil
		}),
	}))
	p.POST("/events", a.eventListener)

	e.Logger.Fatal(e.Start(a.config.ListenAddr))
}

// Render render template.
func (r *template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	t, err := r.View.GetTemplate(name)
	if err != nil {
		panic(fmt.Errorf("Fatal error template file: %s", err))
	}
	vars, ok := data.(jet.VarMap)
	if !ok {
		vars = jet.VarMap{}
	}
	err = t.Execute(w, vars, nil)
	if err != nil {
		panic(fmt.Errorf("Error rendering template %s: %s", name, err))
	}
	return nil
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
	if infoV1 == "" || infoV2 == "" {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/%s/%s", namespace, repo))
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

	data := jet.VarMap{}
	data.Set("namespace", namespace)
	data.Set("repo", repo)
	data.Set("sha256", sha256)
	data.Set("imageSize", imageSize)
	data.Set("tag", gjson.Get(infoV1, "tag").String())
	data.Set("repoPath", gjson.Get(infoV1, "name").String())
	data.Set("created", gjson.Get(gjson.Get(infoV1, "history.0.v1Compatibility").String(), "created").String())
	data.Set("layersCount", layersCount)
	data.Set("layersV2", layersV2)
	data.Set("layersV1", layersV1)

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

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/%s/%s", namespace, repo))
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
	events := registry.GetEvents()
	data := jet.VarMap{}
	data.Set("events", events)

	return c.Render(http.StatusOK, "event_log.html", data)
}

// eventListener listen events from registry.
func (a *apiClient) eventListener(c echo.Context) error {
	registry.ProcessEvents(c.Request(), a.config.EventRetentionDays)
	return c.String(http.StatusOK, "OK")
}
