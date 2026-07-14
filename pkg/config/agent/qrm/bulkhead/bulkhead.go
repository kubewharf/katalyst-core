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

package bulkhead

import (
	"strconv"
	"strings"
)

type BulkheadConfiguration struct {
	BulkheadPrimaryRelPath        string
	BulkheadReclaimRelPaths       []string
	BulkheadReclaimNumaPrefixes   []string
	BulkheadPartitionRelPaths     []string
	EnableBulkheadReclaimSiblings bool

	BulkheadWorkqueueSysfsDir string
	BulkheadWorkqueueNames    []string

	// system_service plugin
	BulkheadSystemRelPath            string
	BulkheadSystemServiceProcfsPath  string
	BulkheadSystemdCommWhitelist     []string
	BulkheadSystemKThreadCommSubstrs []string
}

func NewBulkheadConfiguration() *BulkheadConfiguration {
	return &BulkheadConfiguration{}
}

func FormatBulkheadNUMARel(prefix string, numaID int) string {
	if prefix == "" {
		return ""
	}
	return strings.Trim(prefix+strconv.Itoa(numaID), "/")
}

func (c BulkheadConfiguration) ReclaimPerNUMA(reclaimIdx, numaID int) string {
	if reclaimIdx < 0 || reclaimIdx >= len(c.BulkheadReclaimNumaPrefixes) {
		return ""
	}
	return FormatBulkheadNUMARel(c.BulkheadReclaimNumaPrefixes[reclaimIdx], numaID)
}
