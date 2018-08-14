package registry

import (
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestSortedMapKeys(t *testing.T) {
	a := map[string]string{
		"foo": "bar",
		"abc": "bar",
		"zoo": "bar",
	}
	b := map[string]timeSlice{
		"zoo": []tagData{tagData{name: "1", created: time.Now()}},
		"abc": []tagData{tagData{name: "1", created: time.Now()}},
		"foo": []tagData{tagData{name: "1", created: time.Now()}},
	}
	c := map[string][]string{
		"zoo": []string{"1", "2"},
		"foo": []string{"1", "2"},
		"abc": []string{"1", "2"},
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
			723425120:  "690 MB",
			8534241213: "8 GB",
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
