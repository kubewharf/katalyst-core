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

package sampling

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// MBMonitorAdaptor is the adaptor interface tailored for mbw monitor
type MBMonitorAdaptor interface {
	FakeNumaConfigured() bool
	Init() error
	GlobalStats(ctx context.Context, refreshRate uint64) error

	GetPackageNUMA() map[int][]int
	GetNUMACCD() map[int][]int
	GetMemoryBandwidthOfPackages() []machine.PackageMB
	GetMemoryBandwidthOfNUMAs() []machine.NumaMB
	GetCCDL3Latency() []float64
}
