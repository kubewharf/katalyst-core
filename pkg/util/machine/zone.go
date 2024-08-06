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

package machine

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

var nodeZoneRE = regexp.MustCompile(`(\d+), zone\s+(\w+)`)

func parseNormalZoneInfo(zoneInfoData []byte) ([]NormalZoneInfo, error) {
	var zoneInfo []NormalZoneInfo
	zoneInfoBlocks := bytes.Split(zoneInfoData, []byte("\nNode"))

	for _, block := range zoneInfoBlocks {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}

		var zoneInfoElement NormalZoneInfo
		zoneInfoElement.Node = -1
		lines := strings.Split(string(block), "\n")
		for _, line := range lines {
			if nodeZone := nodeZoneRE.FindStringSubmatch(line); nodeZone != nil {
				if nodeZone[2] == "Normal" {
					zoneInfoElement.Node = int64(StringToUint64(nodeZone[1]))
				}
				continue
			}
			if zoneInfoElement.Node == -1 {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "per-node stats") {
				continue
			}
			parts := strings.Fields(strings.TrimSpace(line))
			if len(parts) < 2 {
				continue
			}
			switch parts[0] {
			case "nr_free_pages":
				zoneInfoElement.Free = StringToUint64(parts[1])
			case "min":
				zoneInfoElement.Min = StringToUint64(parts[1])
			case "low":
				zoneInfoElement.Low = StringToUint64(parts[1])
			case "nr_zone_inactive_file":
				zoneInfoElement.FileInactive = StringToUint64(parts[1])
			}
		}
		if zoneInfoElement.Node != -1 {
			zoneInfo = append(zoneInfo, zoneInfoElement)
		}
	}
	return zoneInfo, nil
}

func StringToUint64(s string) uint64 {
	uintValue, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return uintValue
}
