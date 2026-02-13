//go:build linux
// +build linux

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

package hostwatermark

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func getIntFromFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("empty content in %s", path)
	}
	val, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s failed: %w", path, err)
	}
	return val, nil
}

func setIntToFile(path string, val int64) error {
	content := fmt.Sprintf("%d\n", val)
	return os.WriteFile(path, []byte(content), 0o644)
}

func setIntIfDifferent(path string, target int64) (oldVal, newVal int64, changed bool, err error) {
	oldVal, err = getIntFromFile(path)
	if err != nil {
		return 0, 0, false, err
	}
	if oldVal == target {
		return oldVal, oldVal, false, nil
	}
	if err = setIntToFile(path, target); err != nil {
		return oldVal, oldVal, false, err
	}
	newVal, err = getIntFromFile(path)
	if err != nil {
		return oldVal, oldVal, true, err
	}
	return oldVal, newVal, true, nil
}
