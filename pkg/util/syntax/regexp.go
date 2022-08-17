/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package syntax

import (
	"regexp"
	"strconv"

	"github.com/pkg/errors"
)

// ExtractIntValue extract int value from a string according regexp.
func ExtractIntValue(s string, r *regexp.Regexp) (bool, int, error) {
	matches := r.FindSubmatch([]byte(s))
	if len(matches) == 2 {
		val, err := strconv.ParseInt(string(matches[1]), 10, 32)
		if err != nil {
			return false, -1, errors.Wrap(err, "parse int failed")
		}

		return true, int(val), nil
	}

	return false, -1, nil
}

// ExtractStringValue extract string value from a string according regexp.
func ExtractStringValue(s string, r *regexp.Regexp) (bool, string, error) {
	matches := r.FindSubmatch([]byte(s))
	if len(matches) == 2 {
		return true, string(matches[1]), nil
	}

	return false, "", nil
}
