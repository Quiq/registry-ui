package registry

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/tidwall/gjson"
)

type PurgeTagsConfig struct {
	DryRun           bool
	KeepDays         int
	KeepMinCount     int
	KeepTagRegexp    string
	KeepTagsFromFile gjson.Result
}

type tagData struct {
	name    string
	created time.Time
}

func (t tagData) String() string {
	return fmt.Sprintf(`"%s <%s>"`, t.name, t.created.Format("2006-01-02 15:04:05"))
}

type timeSlice []tagData

func (p timeSlice) Len() int {
	return len(p)
}

func (p timeSlice) Less(i, j int) bool {
	if p[i].created.Equal(p[j].created) {
		return p[i].name > p[j].name
	} else {
		return p[i].created.After(p[j].created)
	}
}

func (p timeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// PurgeOldTags purge old tags.
func PurgeOldTags(client *Client, config *PurgeTagsConfig) {
	logger := SetupLogging("registry.tasks.PurgeOldTags")
	dryRunText := ""
	if config.DryRun {
		logger.Warn("Dry-run mode enabled.")
		dryRunText = "skipped"
	}
	logger.Info("Scanning registry for repositories, tags and their creation dates...")
	catalog := client.Repositories(true)
	// catalog := map[string][]string{"library": []string{""}}
	now := time.Now().UTC()
	repos := map[string]timeSlice{}
	count := 0
	for namespace := range catalog {
		count = count + len(catalog[namespace])
		for _, repo := range catalog[namespace] {
			if namespace != "library" {
				repo = fmt.Sprintf("%s/%s", namespace, repo)
			}

			tags := client.Tags(repo)
			logger.Infof("[%s] scanning %d tags...", repo, len(tags))
			if len(tags) == 0 {
				continue
			}
			for _, tag := range tags {
				_, infoV1, _ := client.TagInfo(repo, tag, true)
				if infoV1 == "" {
					logger.Errorf("[%s] missing manifest v1 for tag %s", repo, tag)
					continue
				}
				created := gjson.Get(gjson.Get(infoV1, "history.0.v1Compatibility").String(), "created").Time()
				repos[repo] = append(repos[repo], tagData{name: tag, created: created})
			}
		}
	}

	logger.Infof("Scanned %d repositories.", count)
	logger.Infof("Filtering out tags for purging: keep %d days, keep count %d", config.KeepDays, config.KeepMinCount)
	if config.KeepTagRegexp != "" {
		logger.Infof("Keeping tags matching regexp: %s", config.KeepTagRegexp)
	}
	if config.KeepTagsFromFile.IsObject() {
		logger.Infof("Keeping tags for repos from the file: %+v", config.KeepTagsFromFile)
	}
	purgeTags := map[string][]string{}
	keepTags := map[string][]string{}
	count = 0
	for _, repo := range SortedMapKeys(repos) {
		// Sort tags by "created" from newest to oldest.
		sort.Sort(repos[repo])

		// Prep the list of tags to preserve if defined in the file
		tagsFromFile := []string{}
		for _, i := range config.KeepTagsFromFile.Get(repo).Array() {
			tagsFromFile = append(tagsFromFile, i.String())
		}

		// Filter out tags
		for _, tag := range repos[repo] {
			daysOld := int(now.Sub(tag.created).Hours() / 24)
			keepByRegexp := false
			if config.KeepTagRegexp != "" {
				keepByRegexp, _ = regexp.MatchString(config.KeepTagRegexp, tag.name)
			}

			if daysOld > config.KeepDays && !keepByRegexp && !ItemInSlice(tag.name, tagsFromFile) {
				purgeTags[repo] = append(purgeTags[repo], tag.name)
			} else {
				keepTags[repo] = append(keepTags[repo], tag.name)
			}
		}

		// Keep minimal count of tags no matter how old they are.
		if len(keepTags[repo]) < config.KeepMinCount {
			// At least "threshold"-"keep" but not more than available for "purge".
			takeFromPurge := int(math.Min(float64(config.KeepMinCount-len(keepTags[repo])), float64(len(purgeTags[repo]))))
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
		if config.DryRun {
			continue
		}
		for _, tag := range purgeTags[repo] {
			client.DeleteTag(repo, tag)
		}
	}
	logger.Info("Done.")
}
