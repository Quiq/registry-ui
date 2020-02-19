package registry

import (
	"crypto"
	"crypto/tls"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parnurzeal/gorequest"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const userAgent = "docker-registry-ui"

// Client main class.
type Client struct {
	url       string
	verifyTLS bool
	username  string
	password  string
	request   *gorequest.SuperAgent
	logger    *logrus.Entry
	mux       sync.Mutex
	tokens    map[string]string
	repos     map[string][]string
	tagCounts map[string]int
	authURL   string
}

// NewClient initialize Client.
func NewClient(url string, verifyTLS bool, username, password string) *Client {
	c := &Client{
		url:       strings.TrimRight(url, "/"),
		verifyTLS: verifyTLS,
		username:  username,
		password:  password,

		request:   gorequest.New().TLSClientConfig(&tls.Config{InsecureSkipVerify: !verifyTLS}),
		logger:    SetupLogging("registry.client"),
		tokens:    map[string]string{},
		repos:     map[string][]string{},
		tagCounts: map[string]int{},
	}
	resp, _, errs := c.request.Get(c.url+"/v2/").
		Set("User-Agent", userAgent).End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
		return nil
	}

	authHeader := ""
	if resp.StatusCode == 200 {
		return c
	} else if resp.StatusCode == 401 {
		authHeader = resp.Header.Get("WWW-Authenticate")
	} else {
		c.logger.Error(resp.Status)
		return nil
	}

	if strings.HasPrefix(authHeader, "Bearer") {
		r, _ := regexp.Compile(`^Bearer realm="(http.+)",service="(.+)"`)
		if m := r.FindStringSubmatch(authHeader); len(m) > 0 {
			c.authURL = fmt.Sprintf("%s?service=%s", m[1], m[2])
			c.logger.Info("Token auth service discovered at ", c.authURL)
		}
		if c.authURL == "" {
			c.logger.Warn("No token auth service discovered from ", c.url)
			return nil
		}
	} else if strings.HasPrefix(strings.ToLower(authHeader), "basic") {
		c.request = c.request.SetBasicAuth(c.username, c.password)
		c.logger.Info("It was discovered the registry is configured with HTTP basic auth.")
	}

	return c
}

// getToken get existing or new auth token.
func (c *Client) getToken(scope string) string {
	// Check if we have already a token and it's not expired.
	if token, ok := c.tokens[scope]; ok {
		resp, _, _ := c.request.Get(c.url+"/v2/").
			Set("Authorization", fmt.Sprintf("Bearer %s", token)).
			Set("User-Agent", userAgent).End()
		if resp != nil && resp.StatusCode == 200 {
			return token
		}
	}

	request := gorequest.New().TLSClientConfig(&tls.Config{InsecureSkipVerify: !c.verifyTLS})
	resp, data, errs := request.Get(fmt.Sprintf("%s&scope=%s", c.authURL, scope)).
		SetBasicAuth(c.username, c.password).
		Set("User-Agent", userAgent).End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
		return ""
	}
	if resp.StatusCode != 200 {
		c.logger.Error("Failed to get token for scope ", scope, " from ", c.authURL)
		return ""
	}

	token := gjson.Get(data, "token").String()
	// Fix for docker_auth v1.5.0 only
	if token == "" {
		token = gjson.Get(data, "access_token").String()
	}

	c.tokens[scope] = token
	c.logger.Debugf("Received new token for scope %s", scope)

	return c.tokens[scope]
}

// callRegistry make an HTTP request to retrieve data from Docker registry.
func (c *Client) callRegistry(uri, scope, manifestFormat string) (string, gorequest.Response) {
	acceptHeader := fmt.Sprintf("application/vnd.docker.distribution.%s+json", manifestFormat)
	authHeader := ""
	if c.authURL != "" {
		authHeader = fmt.Sprintf("Bearer %s", c.getToken(scope))
	}

	resp, data, errs := c.request.Get(c.url+uri).
		Set("Accept", acceptHeader).
		Set("Authorization", authHeader).
		Set("User-Agent", userAgent).End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
		return "", resp
	}

	c.logger.Debugf("GET %s %s", uri, resp.Status)
	// Returns 404 when no tags in the repo.
	if resp.StatusCode != 200 {
		return "", resp
	}

	// Ensure Docker-Content-Digest header is present as we use it in various places.
	// The header is probably in AWS ECR case.
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Try to get digest from body instead, should be equal to what would be presented in Docker-Content-Digest.
		h := crypto.SHA256.New()
		h.Write([]byte(data))
		resp.Header.Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", h.Sum(nil)))
	}
	return data, resp
}

// Namespaces list repo namespaces.
func (c *Client) Namespaces() []string {
	namespaces := make([]string, 0, len(c.repos))
	for k := range c.repos {
		namespaces = append(namespaces, k)
	}
	if !ItemInSlice("library", namespaces) {
		namespaces = append(namespaces, "library")
	}
	sort.Strings(namespaces)
	return namespaces
}

// Repositories list repos by namespaces where 'library' is the default one.
func (c *Client) Repositories(useCache bool) map[string][]string {
	// Return from cache if available.
	if len(c.repos) > 0 && useCache {
		return c.repos
	}

	c.mux.Lock()
	defer c.mux.Unlock()

	linkRegexp := regexp.MustCompile("^<(.*?)>;.*$")
	scope := "registry:catalog:*"
	uri := "/v2/_catalog"
	c.repos = map[string][]string{}
	for {
		data, resp := c.callRegistry(uri, scope, "manifest.v2")
		if data == "" {
			return c.repos
		}

		for _, r := range gjson.Get(data, "repositories").Array() {
			namespace := "library"
			repo := r.String()
			if strings.Contains(repo, "/") {
				f := strings.SplitN(repo, "/", 2)
				namespace = f[0]
				repo = f[1]
			}
			c.repos[namespace] = append(c.repos[namespace], repo)
		}

		// pagination
		linkHeader := resp.Header.Get("Link")
		link := linkRegexp.FindStringSubmatch(linkHeader)
		if len(link) == 2 {
			// update uri and query next page
			uri = link[1]
		} else {
			// no more pages
			break
		}
	}
	return c.repos
}

// Tags get tags for the repo.
func (c *Client) Tags(repo string) []string {
	scope := fmt.Sprintf("repository:%s:*", repo)
	data, _ := c.callRegistry(fmt.Sprintf("/v2/%s/tags/list", repo), scope, "manifest.v2")
	var tags []string
	for _, t := range gjson.Get(data, "tags").Array() {
		tags = append(tags, t.String())
	}
	return tags
}

// ManifestList gets manifest list entries for a tag for the repo.
func (c *Client) ManifestList(repo, tag string) (string, []gjson.Result) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	uri := fmt.Sprintf("/v2/%s/manifests/%s", repo, tag)
	// If manifest.list.v2 does not exist because it's a normal image,
	// the registry returns manifest.v1 or manifest.v2 if requested by sha256.
	info, resp := c.callRegistry(uri, scope, "manifest.list.v2")
	digest := resp.Header.Get("Docker-Content-Digest")
	sha256 := ""
	if digest != "" {
		sha256 = digest[7:]
	}
	c.logger.Debugf(`Received manifest.list.v2 with sha256 "%s" from %s: %s`, sha256, uri, info)
	return sha256, gjson.Get(info, "manifests").Array()
}

// TagInfo get image info for the repo tag or digest sha256.
func (c *Client) TagInfo(repo, tag string, v1only bool) (string, string, string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	uri := fmt.Sprintf("/v2/%s/manifests/%s", repo, tag)
	// Note, if manifest.v1 does not exist because the image is requested by sha256,
	// the registry returns manifest.v2 instead or manifest.list.v2 if it's the manifest list!
	infoV1, _ := c.callRegistry(uri, scope, "manifest.v1")
	c.logger.Debugf("Received manifest.v1 from %s: %s", uri, infoV1)
	if infoV1 == "" || v1only {
		return "", infoV1, ""
	}

	// Note, if manifest.v2 does not exist because the image is in the older format (Docker 1.9),
	// the registry returns manifest.v1 instead or manifest.list.v2 if it's the manifest list requested by sha256!
	infoV2, resp := c.callRegistry(uri, scope, "manifest.v2")
	c.logger.Debugf("Received manifest.v2 from %s: %s", uri, infoV2)
	digest := resp.Header.Get("Docker-Content-Digest")
	if infoV2 == "" || digest == "" {
		return "", "", ""
	}

	sha256 := digest[7:]
	c.logger.Debugf("sha256 for %s/%s is %s", repo, tag, sha256)
	return sha256, infoV1, infoV2
}

// TagCounts return map with tag counts.
func (c *Client) TagCounts() map[string]int {
	return c.tagCounts
}

// CountTags count repository tags in background regularly.
func (c *Client) CountTags(interval uint8) {
	for {
		start := time.Now()
		c.logger.Info("[CountTags] Calculating image tags...")
		catalog := c.Repositories(false)
		for n, repos := range catalog {
			for _, r := range repos {
				repoPath := r
				if n != "library" {
					repoPath = fmt.Sprintf("%s/%s", n, r)
				}
				c.tagCounts[fmt.Sprintf("%s/%s", n, r)] = len(c.Tags(repoPath))
			}
		}
		c.logger.Infof("[CountTags] Job complete (%v).", time.Now().Sub(start))
		time.Sleep(time.Duration(interval) * time.Minute)
	}
}

// DeleteTag delete image tag.
func (c *Client) DeleteTag(repo, tag string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	// Get sha256 digest for tag.
	_, resp := c.callRegistry(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, "manifest.v2")

	// Delete by manifest digest reference.
	authHeader := ""
	if c.authURL != "" {
		authHeader = fmt.Sprintf("Bearer %s", c.getToken(scope))
	}
	uri := fmt.Sprintf("/v2/%s/manifests/%s", repo, resp.Header.Get("Docker-Content-Digest"))
	resp, _, errs := c.request.Delete(c.url+uri).
		Set("Authorization", authHeader).
		Set("User-Agent", userAgent).End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
	} else {
		// Returns 202 on success.
		if !strings.Contains(repo, "/") {
			c.tagCounts["library/"+repo]--
		} else {
			c.tagCounts[repo]--
		}
		c.logger.Infof("DELETE %s (tag:%s) %s", uri, tag, resp.Status)
	}
}
