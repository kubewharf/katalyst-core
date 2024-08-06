package machine

import (
	"fmt"
	"io/ioutil"
	"regexp"
	"strconv"
)

var (
	memoryCachedRegexp = regexp.MustCompile(`Cached:\s*([0-9]+) kB`)
)

// parseMemoryCached matches a Regexp in a []byte, returning the resulting value in bytes.
// Assumes that the value matched by the Regexp is in KB.
func parseMemoryCached(b []byte, r *regexp.Regexp) (uint64, error) {
	matches := r.FindSubmatch(b)
	if len(matches) != 2 {
		return 0, fmt.Errorf("failed to match regexp in output: %q", string(b))
	}
	m, err := strconv.ParseUint(string(matches[1]), 10, 64)
	if err != nil {
		return 0, err
	}

	// Convert to bytes.
	return m * 1024, err
}

func GetMemoryCached() (uint64, error) {
	out, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMemoryCached(out, memoryCachedRegexp)
}
