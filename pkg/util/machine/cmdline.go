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
	"fmt"
	"os"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	procCmdlinePath = "/proc/cmdline"
	isolCPUsKeyword = "isolcpus="
)

// isolCPUsKnownFlags is the set of well-known non-cpulist flag tokens that may
// appear at the head of the isolcpus= value (e.g. "isolcpus=nohz,domain,managed_irq,1-3").
// They are filtered out before parsing the remaining cpu list.
var isolCPUsKnownFlags = map[string]struct{}{
	"nohz":        {},
	"domain":      {},
	"managed_irq": {},
}

// GetIsolCPUSet parses isolcpus= from /proc/cmdline and returns a CPUSet.
// Supports forms like "isolcpus=1-3,5" or "isolcpus=nohz,domain,managed_irq,1-3".
// Returns an empty CPUSet when isolcpus= is not configured.
//
// On non-Linux platforms /proc/cmdline does not exist, so reading it fails and
// the caller (which only invokes this on Linux init paths) treats the error
// as a degraded empty set.
func GetIsolCPUSet() (CPUSet, error) {
	return parseIsolCPUSetFromCmdlineFile(procCmdlinePath)
}

// parseIsolCPUSetFromCmdlineFile is the testable form of GetIsolCPUSet that
// reads from a caller-supplied cmdline file path. Keeping the IO boundary as
// an explicit parameter avoids global mutable state, so unit tests can run
// safely in parallel.
func parseIsolCPUSetFromCmdlineFile(path string) (CPUSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return NewCPUSet(), fmt.Errorf("read %s failed: %v", path, err)
	}

	general.Infof("kernel cmdline: %s", string(raw))

	for _, token := range strings.Fields(string(raw)) {
		if !strings.HasPrefix(token, isolCPUsKeyword) {
			continue
		}

		value := strings.TrimPrefix(token, isolCPUsKeyword)
		if value == "" {
			return NewCPUSet(), nil
		}

		segments := strings.Split(value, ",")
		cpuListParts := make([]string, 0, len(segments))
		for _, seg := range segments {
			if seg == "" {
				continue
			}
			if _, ok := isolCPUsKnownFlags[seg]; ok {
				continue
			}
			// ignore "key=val" sub-options if any (defensive)
			if strings.Contains(seg, "=") {
				continue
			}
			cpuListParts = append(cpuListParts, seg)
		}

		if len(cpuListParts) == 0 {
			return NewCPUSet(), nil
		}

		cs, err := Parse(strings.Join(cpuListParts, ","))
		if err != nil {
			return NewCPUSet(), fmt.Errorf("parse isolcpus value %q failed: %v", value, err)
		}
		return cs, nil
	}

	return NewCPUSet(), nil
}
