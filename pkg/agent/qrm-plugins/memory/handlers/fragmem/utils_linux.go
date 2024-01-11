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
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type nodeFragScoreInfo struct {
	Node  int
	Score int
}

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

func parseFloatValues(str string) []float64 {
	var values []float64
	for _, valStr := range regexp.MustCompile(`\S+`).FindAllString(str, -1) {
		val, _ := strconv.ParseFloat(valStr, 64)
		values = append(values, val)
	}
	return values
}

/*
* Unusable_index and fragmentation_score have the same algorithm logic
* in the kernel side.
* So, we can also calculate it by reading unusable_index
* according to the method in kernel side.
*
* The method of calculating the fragmentation score in kernel:
* https://github.com/torvalds/linux/blob/v6.4/mm/vmstat.c#L1115
* Unusable_index: https://github.com/torvalds/linux/blob/v6.4/mm/vmstat.c#L2134
 */
// GetNumaFragScore parses the fragScoreFile and returns a slice of nodeFragScoreInfo
func GetNumaFragScore(fragScoreFile string) ([]nodeFragScoreInfo, error) {
	file, err := os.Open(fragScoreFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	re := regexp.MustCompile(`^Node ([0-9]+), zone +Normal ([0-9. ]+)`)

	var results []nodeFragScoreInfo
	for scanner.Scan() {
		line := scanner.Text()
		match := re.FindStringSubmatch(line)
		if len(match) == 3 {
			nodeID, err := strconv.Atoi(match[1])
			if err != nil {
				return nil, err
			}
			valuesStr := match[2]
			values := parseFloatValues(valuesStr)

			if len(values) >= 8 {
				total := 0.0
				highOrder := 8
				for i := highOrder; i < len(values); i++ {
					total += values[i]
				}

				average := int(total / float64(len(values)-highOrder) * 1000)
				results = append(results, nodeFragScoreInfo{Node: nodeID, Score: average})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no valid data found in %s", fragScoreFile)
	}
	return results, nil
}

func setHostMemCompact(node int) {
	targetFile := hostMemNodePath + strconv.Itoa(node) + "/compact"
	_ = os.WriteFile(targetFile, []byte(fmt.Sprintf("%d", 1)), 0o644)
}
