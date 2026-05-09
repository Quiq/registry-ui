package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/CloudyKit/jet/v6"
	"github.com/labstack/echo/v4"
	"github.com/quiq/registry-ui/registry"
	"github.com/spf13/viper"
)

const usernameHTTPHeader = "X-WEBAUTH-USER"

func (a *apiClient) setUserPermissions(c echo.Context) jet.VarMap {
	user := c.Request().Header.Get(usernameHTTPHeader)

	data := jet.VarMap{}
	data.Set("user", user)
	admins := viper.GetStringSlice("access_control.admins")
	data.Set("eventsAllowed", viper.GetBool("access_control.anyone_can_view_events") || registry.ItemInSlice(user, admins))
	data.Set("deleteAllowed", viper.GetBool("access_control.anyone_can_delete_tags") || registry.ItemInSlice(user, admins))
	return data
}

func (a *apiClient) viewCatalog(c echo.Context) error {
	repoPath := strings.Trim(c.Param("repoPath"), "/")
	// fmt.Println("repoPath:", repoPath)

	data := a.setUserPermissions(c)
	data.Set("repoPath", repoPath)

	showTags := false
	showImageInfo := false
	allRepoPaths := a.client.GetRepos()
	repos := []string{}
	if repoPath == "" {
		// Show all repos
		for _, r := range allRepoPaths {
			repos = append(repos, strings.Split(r, "/")[0])
		}
	} else if strings.Contains(repoPath, ":") {
		// Show image info
		showImageInfo = true
	} else {
		for _, r := range allRepoPaths {
			if r == repoPath {
				// Show tags
				showTags = true
			}
			if strings.HasPrefix(r, repoPath+"/") {
				// Show sub-repos
				r = strings.TrimPrefix(r, repoPath+"/")
				repos = append(repos, strings.Split(r, "/")[0])
			}
		}
	}

	if showImageInfo {
		// Show image info
		imageInfo, err := a.client.GetImageInfo(repoPath)
		if err != nil {
			basePath := viper.GetString("uri_base_path")
			return c.Redirect(http.StatusSeeOther, basePath)
		}
		data.Set("ii", imageInfo)
		return c.Render(http.StatusOK, "image_info.html", data)
	} else {
		// Show repos, tags or both.
		repos = registry.UniqueSortedSlice(repos)
		tags := []string{}
		areTagsReady := false
		tagsRefreshInfo := ""
		if showTags {
			tags, areTagsReady = a.client.ListTags(repoPath)
			asyncInterval := viper.GetInt("performance.tags_async_refresh_interval")
			maxCount := viper.GetInt("performance.tags_online_refresh_max_count")
			if asyncInterval == 0 {
				tagsRefreshInfo = "Tags for this repo are refreshed online when browsing."
			} else if maxCount == 0 {
				tagsRefreshInfo = fmt.Sprintf("Tags for this repo are refreshed in the background every %d min.", asyncInterval)
			} else if len(tags) <= maxCount {
				tagsRefreshInfo = fmt.Sprintf("Tags for this repo are refreshed in the background every %d min and online when browsing.", asyncInterval)
			} else {
				tagsRefreshInfo = fmt.Sprintf("Tags for this repo are refreshed in the background every %d min. Online refresh is disabled (tag count exceeds %d).", asyncInterval, maxCount)
			}
		}
		data.Set("repos", repos)
		data.Set("showTags", showTags)
		data.Set("isCatalogReady", a.client.IsCatalogReady())
		data.Set("areTagsReady", areTagsReady)
		data.Set("tagsRefreshInfo", tagsRefreshInfo)
		data.Set("tagCounts", a.client.SubRepoTagCounts(repoPath, repos))
		data.Set("tags", tags)

		pageSize := viper.GetInt("performance.catalog_page_size")
		data.Set("repoItemPerPage", pageSize)
		if repoPath != "" && (len(repos) > 0 || len(tags) > 0) {
			// Do not show events in the root of catalog.
			data.Set("events", a.eventListener.GetEvents(repoPath))
		}
		return c.Render(http.StatusOK, "catalog.html", data)
	}
}

func (a *apiClient) deleteTag(c echo.Context) error {
	repoPath := c.QueryParam("repoPath")
	tag := c.QueryParam("tag")

	data := a.setUserPermissions(c)
	if data["deleteAllowed"].Bool() {
		a.client.DeleteTag(repoPath, tag)
	}
	basePath := viper.GetString("uri_base_path")
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("%s%s", basePath, repoPath))
}

// viewLog view events from sqlite.
func (a *apiClient) viewEventLog(c echo.Context) error {
	data := a.setUserPermissions(c)
	data.Set("events", a.eventListener.GetEvents(""))
	return c.Render(http.StatusOK, "event_log.html", data)
}

// viewStatistics view registry statistics.
func (a *apiClient) viewStatistics(c echo.Context) error {
	data := a.setUserPermissions(c)
	data.Set("isCatalogReady", a.client.IsCatalogReady())
	data.Set("repoCount", len(a.client.GetRepos()))
	data.Set("tagCount", a.client.GetTotalTagCount())
	data.Set("eventCount", a.eventListener.GetEventCount())
	data.Set("topRepos", a.client.GetTopReposByTagCount(10))
	catalogJobInfo, tagsJobInfo := a.client.GetJobInfo()
	data.Set("catalogJobInfo", catalogJobInfo)
	data.Set("tagsJobInfo", tagsJobInfo)
	return c.Render(http.StatusOK, "statistics.html", data)
}

type configSection struct {
	Name    string
	Options [][2]string // key, value pairs
}

// viewOptions view configuration options.
func (a *apiClient) viewOptions(c echo.Context) error {
	data := a.setUserPermissions(c)

	sensitiveKeys := map[string]bool{
		"registry.password":                true,
		"registry.password_file":           true,
		"event_listener.bearer_token":      true,
		"event_listener.database_location": true,
	}

	settings := viper.AllSettings()
	sectionMap := map[string][][2]string{}
	for sectionName, v := range settings {
		if nested, ok := v.(map[string]interface{}); ok {
			keys := make([]string, 0, len(nested))
			for k := range nested {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val := fmt.Sprintf("%v", nested[k])
				if sensitiveKeys[sectionName+"."+k] {
					val = "***"
				}
				sectionMap[sectionName] = append(sectionMap[sectionName], [2]string{k, val})
			}
		} else {
			sectionMap["general"] = append(sectionMap["general"], [2]string{sectionName, fmt.Sprintf("%v", v)})
		}
	}

	sectionNames := make([]string, 0, len(sectionMap))
	for name := range sectionMap {
		sectionNames = append(sectionNames, name)
	}
	sort.Strings(sectionNames)

	sections := make([]configSection, 0, len(sectionNames))
	for _, name := range sectionNames {
		sections = append(sections, configSection{Name: name, Options: sectionMap[name]})
	}

	data.Set("sections", sections)
	return c.Render(http.StatusOK, "options.html", data)
}

// receiveEvents receive events.
func (a *apiClient) receiveEvents(c echo.Context) error {
	a.eventListener.ProcessEvents(c.Request())
	return c.String(http.StatusOK, "OK")
}
