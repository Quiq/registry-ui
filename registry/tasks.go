package registry

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/tidwall/gjson"
)

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
	return p[i].created.After(p[j].created)
}

func (p timeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// PurgeOldTags purge old tags.
func PurgeOldTags(client *Client, purgeDryRun bool, purgeTagsKeepDays, purgeTagsKeepCount int, purgeTagsKeepRegexp string) {
	logger := SetupLogging("registry.tasks.PurgeOldTags")
	dryRunText := ""
	if purgeDryRun {
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
	logger.Info("Filtering out tags for purging...")
	purgeTags := map[string][]string{}
	keepTags := map[string][]string{}
	count = 0
	for _, repo := range SortedMapKeys(repos) {
		// Sort tags by "created" from newest to oldest.
		sortedTags := make(timeSlice, 0, len(repos[repo]))
		for _, d := range repos[repo] {
			sortedTags = append(sortedTags, d)
		}
		sort.Sort(sortedTags)
		repos[repo] = sortedTags

		// Filter out tags by retention days and regexp
		for _, tag := range repos[repo] {
			regexpMatch, _ := regexp.MatchString(purgeTagsKeepRegexp, tag.name)
			delta := int(now.Sub(tag.created).Hours() / 24)
			if !regexpMatch && delta > purgeTagsKeepDays {
				purgeTags[repo] = append(purgeTags[repo], tag.name)
			} else {
				keepTags[repo] = append(keepTags[repo], tag.name)
			}
		}

		// Keep minimal count of tags no matter how old they are.
		if len(repos[repo])-len(purgeTags[repo]) < purgeTagsKeepCount {
			if len(purgeTags[repo]) > purgeTagsKeepCount {
				keepTags[repo] = append(keepTags[repo], purgeTags[repo][:purgeTagsKeepCount]...)
				purgeTags[repo] = purgeTags[repo][purgeTagsKeepCount:]
			} else {
				keepTags[repo] = append(keepTags[repo], purgeTags[repo]...)
				delete(purgeTags, repo)
			}
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
