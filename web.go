package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/CloudyKit/jet"
	"github.com/labstack/echo/v4"
	"github.com/quiq/docker-registry-ui/registry"
	"github.com/tidwall/gjson"
)

func (a *apiClient) viewRepositories(c echo.Context) error {
	namespace := c.Param("namespace")
	if namespace == "" {
		namespace = "library"
	}

	repos := a.client.Repositories(true)[namespace]
	data := a.dataWithPermissions(c)
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

	data := a.dataWithPermissions(c)
	data.Set("namespace", namespace)
	data.Set("repo", repo)
	data.Set("tags", tags)
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

	// Retrieve full image info from various versions of manifests
	sha256, infoV1, infoV2 := a.client.TagInfo(repoPath, tag, false)
	sha256list, manifests := a.client.ManifestList(repoPath, tag)
	if (infoV1 == "" || infoV2 == "") && len(manifests) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s/%s/%s", a.config.BasePath, namespace, repo))
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
				r["digest"] = fmt.Sprintf(`<a href="%s/%s/%s/%s">%s</a>`, a.config.BasePath, namespace, repo, r["digest"], r["digest"])
			}
		} else {
			// Sub-image of the cache type.
			r["size"] = s.Get("size").Int()
		}
		r["ordered_keys"] = registry.SortedMapKeys(r)
		digestList = append(digestList, r)
	}

	// Populate template vars
	data := a.dataWithPermissions(c)
	data.Set("namespace", namespace)
	data.Set("repo", repo)
	data.Set("tag", tag)
	data.Set("repoPath", repoPath)
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

// dataWithPermissions returns a jet.VarMap with permission related information
// set
func (a *apiClient) dataWithPermissions(c echo.Context) jet.VarMap {
	user := c.Request().Header.Get("X-WEBAUTH-USER")

	data := jet.VarMap{}
	data.Set("user", user)
	data.Set("deleteAllowed", a.checkDeletePermission(user))
	data.Set("eventsAllowed", a.checkEventsPermission(user))

	return data
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

// checkEventsPermission checks if anyone is allowed to view events or only
// admins
func (a *apiClient) checkEventsPermission(user string) bool {
	eventsAllowed := a.config.AnyoneCanViewEvents
	if !eventsAllowed {
		for _, u := range a.config.Admins {
			if u == user {
				eventsAllowed = true
				break
			}
		}
	}
	return eventsAllowed
}

// viewLog view events from sqlite.
func (a *apiClient) viewLog(c echo.Context) error {
	data := a.dataWithPermissions(c)
	data.Set("events", a.eventListener.GetEvents(""))

	return c.Render(http.StatusOK, "event_log.html", data)
}

// receiveEvents receive events.
func (a *apiClient) receiveEvents(c echo.Context) error {
	a.eventListener.ProcessEvents(c.Request())
	return c.String(http.StatusOK, "OK")
}
