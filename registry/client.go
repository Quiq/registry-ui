package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

const userAgent = "registry-ui"

// JobInfo stores the last run time and duration of a background job.
type JobInfo struct {
	LastRun  time.Time
	Duration time.Duration
}

// Client main class.
type Client struct {
	puller         *remote.Puller
	pusher         *remote.Pusher
	logger         *logrus.Entry
	repos          []string
	tagsCache      map[string][]string
	tagsCacheMux   sync.RWMutex
	isCatalogReady bool
	nameOptions    []name.Option
	catalogJobInfo JobInfo
	tagsJobInfo    JobInfo
}

type ImageInfo struct {
	IsImageIndex   bool
	IsImage        bool
	ImageRefRepo   string
	ImageRefTag    string
	ImageRefDigest string
	MediaType      string
	Platforms      string
	Manifest       map[string]interface{}

	// Image specific
	ImageSize     int64
	Created       time.Time
	ConfigImageID string
	ConfigFile    map[string]interface{}
}

// NewClient initialize Client.
func NewClient() *Client {
	authOpt := authOption()
	transportOpt := transportOption()

	pageSize := viper.GetInt("performance.catalog_page_size")
	puller, _ := remote.NewPuller(authOpt, transportOpt, remote.WithUserAgent(userAgent), remote.WithPageSize(pageSize))
	pusher, _ := remote.NewPusher(authOpt, transportOpt, remote.WithUserAgent(userAgent))

	nameOptions := []name.Option{}
	if viper.GetBool("registry.insecure") {
		nameOptions = append(nameOptions, name.Insecure)
	}

	c := &Client{
		puller:      puller,
		pusher:      pusher,
		logger:      SetupLogging("registry.client"),
		repos:       []string{},
		tagsCache:   map[string][]string{},
		nameOptions: nameOptions,
	}
	return c
}

// Creates a new Option to set the remote client's authenticator.
func authOption() remote.Option {
	if viper.GetBool("registry.auth_with_keychain") {
		return remote.WithAuthFromKeychain(authn.DefaultKeychain)
	}

	username := viper.GetString("registry.username")
	password := viper.GetString("registry.password")

	if password == "" {
		passwdFile := viper.GetString("registry.password_file")
		if _, err := os.Stat(passwdFile); os.IsNotExist(err) {
			panic(err)
		}
		data, err := os.ReadFile(passwdFile)
		if err != nil {
			panic(err)
		}
		password = strings.TrimSuffix(string(data[:]), "\n")
	}

	return remote.WithAuth(&authn.Basic{Username: username, Password: password})
}

// Creates a new Option to set the remote client's TLS configuration.
func transportOption() remote.Option {
	caCertPath := viper.GetString("registry.tls.ca")

	if caCertPath == "" {
		return remote.WithTransport(remote.DefaultTransport)
	}

	certPool, err := x509.SystemCertPool()
	if err != nil {
		panic(err)
	}

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		panic(err)
	}

	if ok := certPool.AppendCertsFromPEM(caCertPEM); !ok {
		panic("Invalid certificate in CA PEM: " + caCertPath)
	}

	certPath := viper.GetString("registry.tls.cert")
	keyPath := viper.GetString("registry.tls.key")

	clientCertificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		panic(err)
	}

	transport := remote.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:      certPool,
		Certificates: []tls.Certificate{clientCertificate},
	}

	return remote.WithTransport(transport)
}

func (c *Client) StartBackgroundJobs() {
	catalogInterval := viper.GetInt("performance.catalog_refresh_interval")
	tagsRefreshInterval := viper.GetInt("performance.tags_async_refresh_interval")
	isStarted := false
	for {
		c.RefreshCatalog()
		if !isStarted && tagsRefreshInterval > 0 {
			// Start after the first catalog refresh
			go c.RefreshTags(tagsRefreshInterval)
			isStarted = true
		}
		if catalogInterval == 0 {
			c.logger.Warn("Catalog refresh is disabled in the config and will not run anymore.")
			break
		}
		time.Sleep(time.Duration(catalogInterval) * time.Minute)
	}

}

func (c *Client) RefreshCatalog() {
	ctx := context.Background()
	start := time.Now()
	c.logger.Info("[RefreshCatalog] Started reading catalog...")
	registry, _ := name.NewRegistry(viper.GetString("registry.hostname"), c.nameOptions...)
	cat, err := c.puller.Catalogger(ctx, registry)
	if err != nil {
		c.logger.Errorf("[RefreshCatalog] Error fetching catalog: %s", err)
		if !c.isCatalogReady {
			os.Exit(1)
		}
		return
	}
	repos := []string{}
	// The library itself does retries under the hood.
	for cat.HasNext() {
		data, err := cat.Next(ctx)
		if err != nil {
			c.logger.Errorf("[RefreshCatalog] Error listing catalog: %s", err)
		}
		if data != nil {
			repos = append(repos, data.Repos...)
			if !c.isCatalogReady {
				c.repos = append(c.repos, data.Repos...)
				c.logger.Debug("[RefreshCatalog] Repo batch received:", data.Repos)
			}
		}
	}

	if len(repos) > 0 {
		c.repos = repos
	} else {
		c.logger.Warn("[RefreshCatalog] Catalog looks empty, preserving previous list if any.")
	}
	c.logger.Debugf("[RefreshCatalog] Catalog: %s", c.repos)
	c.catalogJobInfo = JobInfo{LastRun: time.Now(), Duration: time.Since(start)}
	c.logger.Infof("[RefreshCatalog] Job complete (%v): %d repos found", c.catalogJobInfo.Duration, len(c.repos))
	c.isCatalogReady = true
}

// IsCatalogReady whether catalog is ready for the first time use
func (c *Client) IsCatalogReady() bool {
	return c.isCatalogReady
}

// GetRepos get all repos
func (c *Client) GetRepos() []string {
	return c.repos
}

// GetJobInfo returns the last run info for background jobs.
func (c *Client) GetJobInfo() (catalog, tags JobInfo) {
	return c.catalogJobInfo, c.tagsJobInfo
}

// ListTags get tags for the repo, returns tags and whether they were cached.
// If online refresh is enabled and tag count is below threshold, fetches fresh tags.
// If background refresh is disabled, always fetches online regardless of max count.
func (c *Client) ListTags(repoName string) ([]string, bool) {
	c.tagsCacheMux.RLock()
	cachedTags, exists := c.tagsCache[repoName]
	tagCount := len(cachedTags)
	c.tagsCacheMux.RUnlock()

	maxCount := viper.GetInt("performance.tags_online_refresh_max_count")
	asyncRefreshInterval := viper.GetInt("performance.tags_async_refresh_interval")

	// If background refresh is disabled, always refresh online.
	// If online refresh is enabled and repo is cached and below threshold, refresh online.
	if asyncRefreshInterval == 0 || (maxCount > 0 && exists && tagCount <= maxCount) {
		if tags := c.FetchAndCacheTagsForRepo(repoName); tags != nil {
			return tags, true
		}
		return []string{}, false
	}

	// Return from cache if exists
	if exists {
		return cachedTags, true
	}

	// Return empty array if not cached yet
	return []string{}, false
}

// FetchAndCacheTagsForRepo fetch and cache tags for a specific repo
func (c *Client) FetchAndCacheTagsForRepo(repoName string) []string {
	ctx := context.Background()

	repo, err := name.NewRepository(viper.GetString("registry.hostname")+"/"+repoName, c.nameOptions...)
	if err != nil {
		c.logger.Errorf("Error creating repo reference for %s: %s", repoName, err)
		return nil
	}

	tags, err := c.puller.List(ctx, repo)
	if err != nil {
		c.logger.Errorf("Error listing tags for repo %s: %s", repoName, err)
		return nil
	}

	// Update cache
	c.tagsCacheMux.Lock()
	c.tagsCache[repoName] = tags
	c.tagsCacheMux.Unlock()

	c.logger.Debugf("Cached %d tags for repo %s", len(tags), repoName)
	return tags
}

// GetImageInfo get image info by the reference - tag name or digest sha256.
func (c *Client) GetImageInfo(imageRef string) (ImageInfo, error) {
	ctx := context.Background()
	ref, err := name.ParseReference(viper.GetString("registry.hostname")+"/"+imageRef, c.nameOptions...)
	if err != nil {
		c.logger.Errorf("Error parsing image reference %s: %s", imageRef, err)
		return ImageInfo{}, err
	}
	descr, err := c.puller.Get(ctx, ref)
	if err != nil {
		c.logger.Errorf("Error fetching image reference %s: %s", imageRef, err)
		return ImageInfo{}, err
	}

	ii := ImageInfo{
		ImageRefRepo:   ref.Context().RepositoryStr(),
		ImageRefTag:    ref.Identifier(),
		ImageRefDigest: descr.Digest.String(),
		MediaType:      string(descr.MediaType),
	}
	if descr.MediaType.IsIndex() {
		ii.IsImageIndex = true
	} else if descr.MediaType.IsImage() {
		ii.IsImage = true
	} else {
		c.logger.Errorf("Image reference %s is neither Index nor Image", imageRef)
		return ImageInfo{}, err
	}

	// Performance notes:
	//   puller.Get() → 1 API call (fetches manifest)
	//   descr.Image() → 0 API calls (type conversion)
	//   img.ConfigFile() → 1 API call (fetches config blob)
	//   img.Manifest() → 0 API calls (already fetched on puller.Get())
	//
	if ii.IsImage {
		img, err := descr.Image()
		if err != nil {
			c.logger.Errorf("Cannot convert descriptor to Image for image reference %s: %s", imageRef, err)
			return ImageInfo{}, err
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			c.logger.Errorf("Cannot fetch ConfigFile for image reference %s: %s", imageRef, err)
			return ImageInfo{}, err
		}
		ii.Created = cfg.Created.Time
		if cfg.Created.Time.IsZero() {
			ii.Created = extractCreatedFromAnnotations(img)
		}
		ii.Platforms = getPlatform(cfg.Platform())
		ii.ConfigFile = structToMap(cfg)
		// ImageID is what is shown in the terminal when doing "docker images".
		// This is a config sha256 of the corresponding image manifest (single platform).
		if x, _ := img.ConfigName(); len(x.String()) > 19 {
			ii.ConfigImageID = x.String()[7:19]
		}
		mf, _ := img.Manifest()
		for _, l := range mf.Layers {
			ii.ImageSize += l.Size
		}
		ii.Manifest = structToMap(mf)
	} else if ii.IsImageIndex {
		// In case of Image Index, if we request for Image() > ConfigFile(), it will be resolved
		// to a config of one of the manifests (one of the platforms).
		// It doesn't make a lot of sense, even they are usually identical. Also extra API calls which slows things down.
		imgIdx, err := descr.ImageIndex()
		if err != nil {
			c.logger.Errorf("Cannot convert descriptor to ImageIndex for image reference %s: %s", imageRef, err)
			return ImageInfo{}, err
		}
		IdxMf, _ := imgIdx.IndexManifest()
		platforms := []string{}
		for _, m := range IdxMf.Manifests {
			platforms = append(platforms, getPlatform(m.Platform))
		}
		ii.Platforms = strings.Join(UniqueSortedSlice(platforms), ", ")
		ii.Manifest = structToMap(IdxMf)
	}

	return ii, nil
}

func getPlatform(p *v1.Platform) string {
	if p != nil {
		return p.String()
	}
	return ""
}

// structToMap convert struct to map so it can be formatted as HTML table easily
func structToMap(obj interface{}) map[string]interface{} {
	var res map[string]interface{}
	jsonBytes, _ := json.Marshal(obj)
	json.Unmarshal(jsonBytes, &res)
	return res
}

// GetImageCreated get image created time
func (c *Client) GetImageCreated(imageRef string) time.Time {
	zeroTime := new(time.Time)
	ctx := context.Background()
	ref, err := name.ParseReference(viper.GetString("registry.hostname")+"/"+imageRef, c.nameOptions...)
	if err != nil {
		c.logger.Errorf("Error parsing image reference %s: %s", imageRef, err)
		return *zeroTime
	}
	descr, err := c.puller.Get(ctx, ref)
	if err != nil {
		c.logger.Errorf("Error fetching image reference %s: %s", imageRef, err)
		return *zeroTime
	}
	// In case of ImageIndex, it is resolved to a random sub-image which should be fine.
	img, err := descr.Image()
	if err != nil {
		c.logger.Errorf("Cannot convert descriptor to Image for image reference %s: %s", imageRef, err)
		return *zeroTime
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		c.logger.Errorf("Cannot fetch ConfigFile for image reference %s: %s", imageRef, err)
		return *zeroTime
	}
	if cfg.Created.Time.IsZero() {
		return extractCreatedFromAnnotations(img)
	}

	return cfg.Created.Time
}

func extractCreatedFromAnnotations(img v1.Image) time.Time {
	// Some tools (e.g. cosign without --record-creation-timestamp) produce images
	// with a zero creation time in the config file. As a fallback, try to extract
	// the creation date from the manifest annotation "org.opencontainers.image.created".
	// See https://specs.opencontainers.org/image-spec/annotations/
	zeroTime := new(time.Time)
	if mf, err := img.Manifest(); err == nil && mf.Annotations != nil {
		if createdStr, ok := mf.Annotations["org.opencontainers.image.created"]; ok {
			if createdTime, err := time.Parse(time.RFC3339, createdStr); err == nil {
				return createdTime
			}
		}
	}
	return *zeroTime
}

// GetTotalTagCount returns the total number of tags across all cached repos.
func (c *Client) GetTotalTagCount() int {
	c.tagsCacheMux.RLock()
	defer c.tagsCacheMux.RUnlock()

	total := 0
	for _, tags := range c.tagsCache {
		total += len(tags)
	}
	return total
}

// RepoTagCount represents a repo and its tag count.
type RepoTagCount struct {
	Repo  string
	Count int
}

// GetTopReposByTagCount returns the top N repos sorted by tag count descending.
func (c *Client) GetTopReposByTagCount(n int) []RepoTagCount {
	c.tagsCacheMux.RLock()
	result := make([]RepoTagCount, 0, len(c.tagsCache))
	for repo, tags := range c.tagsCache {
		result = append(result, RepoTagCount{Repo: repo, Count: len(tags)})
	}
	c.tagsCacheMux.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	if len(result) > n {
		result = result[:n]
	}
	return result
}

// SubRepoTagCounts return map with tag counts according to the provided list of repos/sub-repos etc.
func (c *Client) SubRepoTagCounts(repoPath string, repos []string) map[string]int {
	counts := map[string]int{}
	for _, r := range repos {
		subRepo := r
		if repoPath != "" {
			subRepo = repoPath + "/" + r
		}

		// Acquire lock to prevent concurrent map iteration and map write.
		c.tagsCacheMux.RLock()
		for k, v := range c.tagsCache {
			if k == subRepo || strings.HasPrefix(k, subRepo+"/") {
				counts[subRepo] = counts[subRepo] + len(v)
			}
		}
		c.tagsCacheMux.RUnlock()
	}
	return counts
}

// RefreshTags fetch repository tags in background periodically and cache them.
func (c *Client) RefreshTags(interval int) {
	for {
		start := time.Now()
		c.logger.Info("[RefreshTags] Started caching tags for all repositories...")
		for _, r := range c.repos {
			c.FetchAndCacheTagsForRepo(r)
		}
		c.tagsJobInfo = JobInfo{LastRun: time.Now(), Duration: time.Since(start)}
		c.logger.Infof("[RefreshTags] Job complete (%v).", c.tagsJobInfo.Duration)
		time.Sleep(time.Duration(interval) * time.Minute)
	}
}

// DeleteTag delete image tag.
func (c *Client) DeleteTag(repoPath, tag string) {
	ctx := context.Background()
	imageRef := repoPath + ":" + tag
	ref, err := name.ParseReference(viper.GetString("registry.hostname")+"/"+imageRef, c.nameOptions...)
	if err != nil {
		c.logger.Errorf("Error parsing image reference %s: %s", imageRef, err)
		return
	}
	// Get manifest so we have a digest to delete by
	descr, err := c.puller.Get(ctx, ref)
	if err != nil {
		c.logger.Errorf("Error fetching image reference %s: %s", imageRef, err)
		return
	}
	// Parse image reference by digest now
	imageRefDigest := ref.Context().RepositoryStr() + "@" + descr.Digest.String()
	ref, err = name.ParseReference(viper.GetString("registry.hostname")+"/"+imageRefDigest, c.nameOptions...)
	if err != nil {
		c.logger.Errorf("Error parsing image reference %s: %s", imageRefDigest, err)
		return
	}

	// Delete tag using digest.
	// Note, it will also delete any other tags pointing to the same digest!
	err = c.pusher.Delete(ctx, ref)
	if err != nil {
		c.logger.Errorf("Error deleting image %s: %s", imageRef, err)
		return
	}
	// Remove tag from cache
	c.tagsCacheMux.Lock()
	if cachedTags, exists := c.tagsCache[repoPath]; exists {
		updatedTags := []string{}
		for _, t := range cachedTags {
			if t != tag {
				updatedTags = append(updatedTags, t)
			}
		}
		c.tagsCache[repoPath] = updatedTags
	}
	c.tagsCacheMux.Unlock()

	c.logger.Infof("Image %s has been successfully deleted.", imageRef)
}
