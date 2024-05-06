package main

import (
	"fmt"
	"net/http"
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
		if showTags {
			tags = a.client.ListTags(repoPath)

		}
		data.Set("repos", repos)
		data.Set("isCatalogReady", a.client.IsCatalogReady())
		data.Set("tagCounts", a.client.SubRepoTagCounts(repoPath, repos))
		data.Set("tags", tags)
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

// receiveEvents receive events.
func (a *apiClient) receiveEvents(c echo.Context) error {
	a.eventListener.ProcessEvents(c.Request())
	return c.String(http.StatusOK, "OK")
}
