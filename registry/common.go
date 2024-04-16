package registry

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
)

// SetupLogging setup logger
func SetupLogging(name string) *logrus.Entry {
	logrus.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: time.RFC3339,
		FullTimestamp:   true,
	})
	// Output to stdout instead of the default stderr.
	logrus.SetOutput(os.Stdout)

	return logrus.WithFields(logrus.Fields{"logger": name})
}

// SortedMapKeys sort keys of the map where values can be of any type.
func SortedMapKeys(m interface{}) []string {
	v := reflect.ValueOf(m)
	keys := make([]string, 0, len(v.MapKeys()))
	for _, key := range v.MapKeys() {
		keys = append(keys, key.String())
	}
	sort.Strings(keys)
	return keys
}

// PrettySize format bytes in more readable units.
func PrettySize(size float64) string {
	units := []string{"B", "KB", "MB", "GB"}
	i := 0
	for size > 1024 && i < len(units) {
		size = size / 1024
		i = i + 1
	}
	// Format decimals as follow: 0 B, 0 KB, 0.0 MB, 0.00 GB
	decimals := i - 1
	if decimals < 0 {
		decimals = 0
	}
	return fmt.Sprintf("%.*f %s", decimals, size, units[i])
}

// ItemInSlice check if item is an element of slice
func ItemInSlice(item string, slice []string) bool {
	for _, i := range slice {
		if i == item {
			return true
		}
	}
	return false
}

// UniqueSortedSlice filter out duplicate items from slice
func UniqueSortedSlice(slice []string) []string {
	sort.Strings(slice)
	seen := make(map[string]struct{}, len(slice))
	j := 0
	for _, i := range slice {
		if _, ok := seen[i]; ok {
			continue
		}
		seen[i] = struct{}{}
		slice[j] = i
		j++
	}
	return slice[:j]
}
