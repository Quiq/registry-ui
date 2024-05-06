package registry

import (
	"context"
	"encoding/json"
	"os"
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

// Client main class.
type Client struct {
	puller         *remote.Puller
	pusher         *remote.Pusher
	logger         *logrus.Entry
	repos          []string
	tagCountsMux   sync.Mutex
	tagCounts      map[string]int
	isCatalogReady bool
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
	var authOpt remote.Option
	if viper.GetBool("registry.auth_with_keychain") {
		authOpt = remote.WithAuthFromKeychain(authn.DefaultKeychain)
	} else {
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

		authOpt = remote.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: viper.GetString("registry.username"), Password: password,
		}))
	}

	pageSize := viper.GetInt("performance.catalog_page_size")
	puller, _ := remote.NewPuller(authOpt, remote.WithUserAgent(userAgent), remote.WithPageSize(pageSize))
	pusher, _ := remote.NewPusher(authOpt, remote.WithUserAgent(userAgent))

	c := &Client{
		puller:    puller,
		pusher:    pusher,
		logger:    SetupLogging("registry.client"),
		repos:     []string{},
		tagCounts: map[string]int{},
	}
	return c
}

func (c *Client) StartBackgroundJobs() {
	catalogInterval := viper.GetInt("performance.catalog_refresh_interval")
	tagsCountInterval := viper.GetInt("performance.tags_count_refresh_interval")
	isStarted := false
	for {
		c.RefreshCatalog()
		if !isStarted && tagsCountInterval > 0 {
			// Start after the first catalog refresh
			go c.CountTags(tagsCountInterval)
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
	registry, _ := name.NewRegistry(viper.GetString("registry.hostname"))
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
	c.logger.Infof("[RefreshCatalog] Job complete (%v): %d repos found", time.Since(start), len(c.repos))
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

// ListTags get tags for the repo
func (c *Client) ListTags(repoName string) []string {
	ctx := context.Background()
	repo, _ := name.NewRepository(viper.GetString("registry.hostname") + "/" + repoName)
	tags, err := c.puller.List(ctx, repo)
	if err != nil {
		c.logger.Errorf("Error listing tags for repo %s: %s", repoName, err)
	}
	c.tagCountsMux.Lock()
	c.tagCounts[repoName] = len(tags)
	c.tagCountsMux.Unlock()
	return tags
}

// GetImageInfo get image info by the reference - tag name or digest sha256.
func (c *Client) GetImageInfo(imageRef string) (ImageInfo, error) {
	ctx := context.Background()
	ref, err := name.ParseReference(viper.GetString("registry.hostname") + "/" + imageRef)
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

	if ii.IsImage {
		img, _ := descr.Image()
		cfg, err := img.ConfigFile()
		if err != nil {
			c.logger.Errorf("Cannot fetch ConfigFile for image reference %s: %s", imageRef, err)
			return ImageInfo{}, err
		}
		ii.Created = cfg.Created.Time
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
		imgIdx, _ := descr.ImageIndex()
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
	ref, err := name.ParseReference(viper.GetString("registry.hostname") + "/" + imageRef)
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
	img, _ := descr.Image()
	cfg, err := img.ConfigFile()
	if err != nil {
		c.logger.Errorf("Cannot fetch ConfigFile for image reference %s: %s", imageRef, err)
		return *zeroTime
	}
	return cfg.Created.Time
}

// SubRepoTagCounts return map with tag counts according to the provided list of repos/sub-repos etc.
func (c *Client) SubRepoTagCounts(repoPath string, repos []string) map[string]int {
	counts := map[string]int{}
	for _, r := range repos {
		subRepo := r
		if repoPath != "" {
			subRepo = repoPath + "/" + r
		}
		for k, v := range c.tagCounts {
			if k == subRepo || strings.HasPrefix(k, subRepo+"/") {
				counts[subRepo] = counts[subRepo] + v
			}
		}
	}
	return counts
}

// CountTags count repository tags in background regularly.
func (c *Client) CountTags(interval int) {
	for {
		start := time.Now()
		c.logger.Info("[CountTags] Started counting tags...")
		for _, r := range c.repos {
			c.ListTags(r)
		}
		c.logger.Infof("[CountTags] Job complete (%v).", time.Since(start))
		time.Sleep(time.Duration(interval) * time.Minute)
	}
}

// DeleteTag delete image tag.
func (c *Client) DeleteTag(repoPath, tag string) {
	ctx := context.Background()
	imageRef := repoPath + ":" + tag
	ref, err := name.ParseReference(viper.GetString("registry.hostname") + "/" + imageRef)
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
	ref, err = name.ParseReference(viper.GetString("registry.hostname") + "/" + imageRefDigest)
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
	c.tagCountsMux.Lock()
	c.tagCounts[repoPath]--
	c.tagCountsMux.Unlock()
	c.logger.Infof("Image %s has been successfully deleted.", imageRef)
}
