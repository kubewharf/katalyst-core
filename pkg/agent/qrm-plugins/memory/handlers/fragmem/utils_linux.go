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

package fragmem

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
)

// checkCompactionProactivenessDisabled checks the content of /proc/sys/vm/compaction_proactiveness
// and returns false if the file exists and its content is greater than 0, otherwise returns true.
func checkCompactionProactivenessDisabled(filePath string) bool {
	// Check if the file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return true
	}

	// Read the file content
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return true
	}

	// Convert content to integer
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return true
	}

	// Return false if value is greater than 0
	return value <= 0
}

func setHostMemCompact(node int) {
	targetFile := hostMemNodePath + strconv.Itoa(node) + "/compact"
	_ = os.WriteFile(targetFile, []byte(fmt.Sprintf("%d", 1)), 0o644)
}
