//go:build linux

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
	"os"
)

type NormalZoneInfo struct {
	Node         int64
	Free         uint64
	Min          uint64
	Low          uint64
	FileInactive uint64
}

func GetNormalZoneInfo(zoneInfoPath string) []NormalZoneInfo {
	data, err := os.ReadFile(zoneInfoPath)
	if err != nil {
		return nil
	}
	zoneInfo, err := parseNormalZoneInfo(data)
	if err != nil {
		return nil
	}
	return zoneInfo
}
