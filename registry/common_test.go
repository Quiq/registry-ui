package registry

import (
	"math"
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestKeepMinCount(t *testing.T) {
	keepTags := []string{"1.8.15"}
	purgeTags := []string{"1.8.14", "1.8.13", "1.8.12", "1.8.10", "1.8.9", "1.8.8", "1.8.7", "1.8.6", "1.8.5", "1.8.4", "1.8.3"}
	purgeTagsKeepCount := 10

	// Keep minimal count of tags no matter how old they are.
	if len(keepTags) < purgeTagsKeepCount {
		// Min of threshold-keep but not more than purge.
		takeFromPurge := int(math.Min(float64(purgeTagsKeepCount-len(keepTags)), float64(len(purgeTags))))
		keepTags = append(keepTags, purgeTags[:takeFromPurge]...)
		purgeTags = purgeTags[takeFromPurge:]
	}

	convey.Convey("Test keep min count logic", t, func() {
		convey.So(keepTags, convey.ShouldResemble, []string{"1.8.15", "1.8.14", "1.8.13", "1.8.12", "1.8.10", "1.8.9", "1.8.8", "1.8.7", "1.8.6", "1.8.5"})
		convey.So(purgeTags, convey.ShouldResemble, []string{"1.8.4", "1.8.3"})
	})
}

func TestSortedMapKeys(t *testing.T) {
	a := map[string]string{
		"foo": "bar",
		"abc": "bar",
		"zoo": "bar",
	}
	b := map[string]timeSlice{
		"zoo": []tagData{{name: "1", created: time.Now()}},
		"abc": []tagData{{name: "1", created: time.Now()}},
		"foo": []tagData{{name: "1", created: time.Now()}},
	}
	c := map[string][]string{
		"zoo": {"1", "2"},
		"foo": {"1", "2"},
		"abc": {"1", "2"},
	}
	expect := []string{"abc", "foo", "zoo"}
	convey.Convey("Sort map keys", t, func() {
		convey.So(SortedMapKeys(a), convey.ShouldResemble, expect)
		convey.So(SortedMapKeys(b), convey.ShouldResemble, expect)
		convey.So(SortedMapKeys(c), convey.ShouldResemble, expect)
	})
}

func TestPrettySize(t *testing.T) {
	convey.Convey("Format bytes", t, func() {
		input := map[float64]string{
			123:        "123 B",
			23123:      "23 KB",
			23923:      "23 KB",
			723425120:  "689.9 MB",
			8534241213: "7.95 GB",
		}
		for key, val := range input {
			convey.So(PrettySize(key), convey.ShouldEqual, val)
		}
	})
}

func TestItemInSlice(t *testing.T) {
	a := []string{"abc", "def", "ghi"}
	convey.Convey("Check whether element is in slice", t, func() {
		convey.So(ItemInSlice("abc", a), convey.ShouldBeTrue)
		convey.So(ItemInSlice("ghi", a), convey.ShouldBeTrue)
		convey.So(ItemInSlice("abc1", a), convey.ShouldBeFalse)
		convey.So(ItemInSlice("gh", a), convey.ShouldBeFalse)
	})
}
