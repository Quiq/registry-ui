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

	"github.com/hhkbp2/go-logging"
	"github.com/parnurzeal/gorequest"
	"github.com/tidwall/gjson"
)

// Client main class.
type Client struct {
	url       string
	verifyTLS bool
	username  string
	password  string
	request   *gorequest.SuperAgent
	logger    logging.Logger
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
	resp, _, errs := c.request.Get(c.url+"/v2/").Set("User-Agent", "docker-registry-ui").End()
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
		resp, _, _ := c.request.Get(c.url+"/v2/").Set("Authorization", fmt.Sprintf("Bearer %s", token)).Set("User-Agent", "docker-registry-ui").End()
		if resp != nil && resp.StatusCode == 200 {
			return token
		}
	}

	request := gorequest.New().TLSClientConfig(&tls.Config{InsecureSkipVerify: !c.verifyTLS})
	resp, data, errs := request.Get(fmt.Sprintf("%s&scope=%s", c.authURL, scope)).SetBasicAuth(c.username, c.password).Set("User-Agent", "docker-registry-ui").End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
		return ""
	}
	if resp.StatusCode != 200 {
		c.logger.Error("Failed to get token for scope ", scope, " from ", c.authURL)
		return ""
	}

	c.tokens[scope] = gjson.Get(data, "token").String()
	c.logger.Info("Received new token for scope ", scope)

	return c.tokens[scope]
}

// callRegistry make an HTTP request to Docker registry.
func (c *Client) callRegistry(uri, scope string, manifest uint, delete bool) (string, gorequest.Response) {
	acceptHeader := fmt.Sprintf("application/vnd.docker.distribution.manifest.v%d+json", manifest)
	authHeader := ""
	if c.authURL != "" {
		authHeader = fmt.Sprintf("Bearer %s", c.getToken(scope))
	}

	resp, data, errs := c.request.Get(c.url+uri).Set("Accept", acceptHeader).Set("Authorization", authHeader).Set("User-Agent", "docker-registry-ui").End()
	if len(errs) > 0 {
		c.logger.Error(errs[0])
		return "", resp
	}

	c.logger.Info("GET ", uri, " ", resp.Status)
	// Returns 404 when no tags in the repo.
	if resp.StatusCode != 200 {
		return "", resp
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Try to get digest from body instead, should be equal to what would be presented
		// in Docker-Content-Digest
		h := crypto.SHA256.New()
		h.Write([]byte(data))
		resp.Header.Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", h.Sum(nil)))
	}

	if delete {
		// Delete by manifest digest reference.
		parts := strings.Split(uri, "/manifests/")
		uri = parts[0] + "/manifests/" + digest
		resp, _, errs := c.request.Delete(c.url+uri).Set("Accept", acceptHeader).Set("Authorization", authHeader).Set("User-Agent", "docker-registry-ui").End()
		if len(errs) > 0 {
			c.logger.Error(errs[0])
		} else {
			// Returns 202 on success.
			c.logger.Info("DELETE ", uri, " (", parts[1], ") ", resp.Status)
		}
		return "", resp
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
		data, resp := c.callRegistry(uri, scope, 2, false)
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
	data, _ := c.callRegistry(fmt.Sprintf("/v2/%s/tags/list", repo), scope, 2, false)
	var tags []string
	for _, t := range gjson.Get(data, "tags").Array() {
		tags = append(tags, t.String())
	}
	return tags
}

// TagInfo get image info for the repo tag.
func (c *Client) TagInfo(repo, tag string, v1only bool) (rsha256, rinfoV1, rinfoV2 string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	infoV1, _ := c.callRegistry(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, 1, false)
	if infoV1 == "" {
		return "", "", ""
	}

	if v1only {
		return "", infoV1, ""
	}

	infoV2, resp := c.callRegistry(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, 2, false)
	digest := resp.Header.Get("Docker-Content-Digest")
	if infoV2 == "" || digest == "" {
		return "", "", ""
	}

	sha256 := digest[7:]
	return sha256, infoV1, infoV2
}

// TagCounts return map with tag counts.
func (c *Client) TagCounts() map[string]int {
	return c.tagCounts
}

// CountTags count repository tags in background regularly.
func (c *Client) CountTags(interval uint8) {
	for {
		c.logger.Info("Calculating tags in background...")
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
		c.logger.Info("Tags calculation complete.")
		time.Sleep(time.Duration(interval) * time.Minute)
	}
}

// DeleteTag delete image tag.
func (c *Client) DeleteTag(repo, tag string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	c.callRegistry(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, 2, true)
}
