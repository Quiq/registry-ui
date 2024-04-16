package registry

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/tidwall/gjson"
)

type TagData struct {
	name    string
	created time.Time
}

func (t TagData) String() string {
	return fmt.Sprintf(`"%s <%s>"`, t.name, t.created.Format("2006-01-02 15:04:05"))
}

type timeSlice []TagData

func (p timeSlice) Len() int {
	return len(p)
}

func (p timeSlice) Less(i, j int) bool {
	// reverse sort tags on name if equal dates (OCI image case)
	// see https://github.com/Quiq/registry-ui/pull/62
	if p[i].created.Equal(p[j].created) {
		return p[i].name > p[j].name
	}
	return p[i].created.After(p[j].created)
}

func (p timeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// PurgeOldTags purge old tags.
func PurgeOldTags(client *Client, purgeDryRun bool, purgeFromRepos string) {
	logger := SetupLogging("registry.tasks.PurgeOldTags")

	var dataFromFile gjson.Result
	keepFromFile := viper.GetString("purge_tags.keep_from_file")
	if keepFromFile != "" {
		if _, err := os.Stat(keepFromFile); os.IsNotExist(err) {
			logger.Warnf("Cannot open %s: %s", keepFromFile, err)
			logger.Error("Not purging anything!")
			return
		}
		data, err := os.ReadFile(keepFromFile)
		if err != nil {
			logger.Warnf("Cannot read %s: %s", keepFromFile, err)
			logger.Error("Not purging anything!")
			return
		}
		dataFromFile = gjson.ParseBytes(data)
	}

	dryRunText := ""
	if purgeDryRun {
		logger.Warn("Dry-run mode enabled.")
		dryRunText = "skipped"
	}

	catalog := []string{}
	if purgeFromRepos != "" {
		logger.Infof("Working on repositories [%s] to scan their tags and creation dates...", purgeFromRepos)
		catalog = append(catalog, strings.Split(purgeFromRepos, ",")...)
	} else {
		logger.Info("Scanning registry for repositories, tags and their creation dates...")
		client.RefreshCatalog()
		catalog = client.GetRepos()
	}

	now := time.Now().UTC()
	repos := map[string]timeSlice{}
	count := 0
	for _, repo := range catalog {
		tags := client.ListTags(repo)
		if len(tags) == 0 {
			continue
		}
		logger.Infof("[%s] scanning %d tags...", repo, len(tags))
		for _, tag := range tags {
			imageRef := repo + ":" + tag
			created := client.GetImageCreated(imageRef)
			if created.IsZero() {
				// Image manifest with zero creation time, e.g. cosign one
				logger.Debugf("[%s] tag with zero creation time: %s", repo, tag)
				continue
			}
			repos[repo] = append(repos[repo], TagData{name: tag, created: created})
		}
	}

	logger.Infof("Scanned %d repositories.", len(catalog))

	keepDays := viper.GetInt("purge_tags.keep_days")
	keepCount := viper.GetInt("purge_tags.keep_count")
	logger.Infof("Filtering out tags for purging: keep %d days, keep count %d", keepDays, keepCount)
	keepRegexp := viper.GetString("purge_tags.keep_regexp")
	if keepRegexp != "" {
		logger.Infof("Keeping tags matching regexp: %s", keepRegexp)
	}
	if keepFromFile != "" {
		logger.Infof("Keeping tags for repos from the file: %+v", dataFromFile)
	}
	purgeTags := map[string][]string{}
	keepTags := map[string][]string{}
	count = 0
	for _, repo := range SortedMapKeys(repos) {
		// Sort tags by "created" from newest to oldest.
		sort.Sort(repos[repo])

		// Prep the list of tags to preserve if defined in the file
		tagsFromFile := []string{}
		for _, i := range dataFromFile.Get(repo).Array() {
			tagsFromFile = append(tagsFromFile, i.String())
		}

		// Filter out tags
		for _, tag := range repos[repo] {
			daysOld := int(now.Sub(tag.created).Hours() / 24)
			matchByRegexp := false
			if keepRegexp != "" {
				matchByRegexp, _ = regexp.MatchString(keepRegexp, tag.name)
			}

			if daysOld > keepDays && !matchByRegexp && !ItemInSlice(tag.name, tagsFromFile) {
				purgeTags[repo] = append(purgeTags[repo], tag.name)
			} else {
				keepTags[repo] = append(keepTags[repo], tag.name)
			}
		}

		// Keep minimal count of tags no matter how old they are.
		if len(keepTags[repo]) < keepCount {
			// At least "threshold"-"keep" but not more than available for "purge".
			takeFromPurge := int(math.Min(float64(keepCount-len(keepTags[repo])), float64(len(purgeTags[repo]))))
			keepTags[repo] = append(keepTags[repo], purgeTags[repo][:takeFromPurge]...)
			purgeTags[repo] = purgeTags[repo][takeFromPurge:]
		}

		count = count + len(purgeTags[repo])
		logger.Infof("[%s] All %d: %v", repo, len(repos[repo]), repos[repo])
		logger.Infof("[%s] Keep %d: %v", repo, len(keepTags[repo]), keepTags[repo])
		logger.Infof("[%s] Purge %d: %v", repo, len(purgeTags[repo]), purgeTags[repo])
	}

	logger.Infof("There are %d tags to purge.", count)
	if count > 0 {
		logger.Info("Purging old tags...")
	}

	for _, repo := range SortedMapKeys(purgeTags) {
		if len(purgeTags[repo]) == 0 {
			continue
		}
		logger.Infof("[%s] Purging %d tags... %s", repo, len(purgeTags[repo]), dryRunText)
		if purgeDryRun {
			continue
		}
		for _, tag := range purgeTags[repo] {
			client.DeleteTag(repo, tag)
		}
	}
	logger.Info("Done.")
}
