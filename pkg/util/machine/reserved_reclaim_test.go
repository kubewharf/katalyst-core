/*
Copyright 2026 The Katalyst Authors.

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
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

func evenCapacity(size int) func(int) int {
	return func(int) int { return size }
}

func sparseNUMATestTopology(t *testing.T) *CPUTopology {
	t.Helper()

	topology, err := GenerateDummyCPUTopology(8, 1, 2)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}
	for cpuID, cpuInfo := range topology.CPUDetails {
		if cpuInfo.NUMANodeID == 0 {
			cpuInfo.NUMANodeID = 2
		} else {
			cpuInfo.NUMANodeID = 7
		}
		topology.CPUDetails[cpuID] = cpuInfo
	}
	topology.NUMAToCPUs = NUMANodeInfo{
		2: topology.CPUDetails.CPUsInNUMANodes(2),
		7: topology.CPUDetails.CPUsInNUMANodes(7),
	}
	return topology
}

func TestResolvePerNUMAReservedForReclaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		conf      *dynamicconfig.Configuration
		cpuNum    int
		socketNum int
		numaNum   int
		want      map[int]int
	}{
		{
			// SMT2 dummy topology (cpusPerCore=2): ratio takes priority, then the
			// per-NUMA magnitude rounds UP to a complete core. ceil(0.1*10)=1,
			// max(floor 1,1)=1, rounded up to 2 CPUs (1 core).
			name:      "ratio priority over global",
			conf:      newReclaimTestConfig(0, "100", "0.1", "1"),
			cpuNum:    40,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
		{
			// ratio computes above the floor: ceil(0.5*10)=5 wins over floor 2,
			// rounded UP to 6 CPUs (3 cores) on SMT2.
			name:      "ratio above floor",
			conf:      newReclaimTestConfig(0, "", "0.5", "2"),
			cpuNum:    40,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 6, 1: 6, 2: 6, 3: 6},
		},
		{
			// floor dominates when ratio result is smaller: ceil(0.1*10)=1 < floor
			// 3, so 3, rounded UP to 4 CPUs (2 cores) on SMT2.
			name:      "floor dominates ratio",
			conf:      newReclaimTestConfig(0, "", "0.1", "3"),
			cpuNum:    40,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 4, 1: 4, 2: 4, 3: 4},
		},
		{
			// ratio zero: fall back to global cores distributed evenly.
			// 8 cores over 4 NUMA -> 2 per NUMA (already one core on SMT2);
			// per-NUMA floor ignored on fallback.
			name:      "global fallback even",
			conf:      newReclaimTestConfig(0, "8", "", "5"),
			cpuNum:    40,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
		{
			// global fallback clamps to numCPUs before even distribution.
			// 999 clamped to 8 CPUs over 4 NUMA -> 2 per NUMA (one core on SMT2).
			name:      "global fallback clamps to numCPUs",
			conf:      newReclaimTestConfig(0, "999", "", ""),
			cpuNum:    8,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
		{
			// global fallback lifts below-NUMA-count reserve to one per NUMA, then
			// rounds each up to a complete core: 1 CPU -> 2 CPUs on SMT2.
			name:      "global fallback lifts to numNUMANodes",
			conf:      newReclaimTestConfig(0, "1", "", ""),
			cpuNum:    40,
			socketNum: 1,
			numaNum:   4,
			want:      map[int]int{0: 2, 1: 2, 2: 2, 3: 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			topology, err := GenerateDummyCPUTopology(tt.cpuNum, tt.socketNum, tt.numaNum)
			if err != nil {
				t.Fatalf("generate dummy topology: %v", err)
			}
			got := ResolvePerNUMAReservedForReclaim(tt.conf, topology)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ResolvePerNUMAReservedForReclaim() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolvePerNUMAReservedForReclaimUsesRealNUMAIDs(t *testing.T) {
	t.Parallel()

	topology := sparseNUMATestTopology(t)
	tests := []struct {
		name  string
		conf  *dynamicconfig.Configuration
		floor int
	}{
		{
			name:  "global floor",
			conf:  newReclaimTestConfig(0, "5", "", ""),
			floor: 5,
		},
		{
			name:  "numa ratio",
			conf:  newReclaimTestConfig(0, "0", "0.25", "0"),
			floor: 4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ResolvePerNUMAReservedForReclaim(tt.conf, topology)
			if _, ok := got[2]; !ok {
				t.Fatalf("missing real NUMA ID 2 in %v", got)
			}
			if _, ok := got[7]; !ok {
				t.Fatalf("missing real NUMA ID 7 in %v", got)
			}
			if len(got) != 2 {
				t.Fatalf("ResolvePerNUMAReservedForReclaim() keys = %v, want only [2 7]", got)
			}

			total := 0
			for numaID, target := range got {
				if target%topology.CPUsPerCore() != 0 {
					t.Fatalf("NUMA ID %d target %d is not core aligned", numaID, target)
				}
				total += target
			}
			if total < tt.floor {
				t.Fatalf("aggregate target %d is below configured floor %d", total, tt.floor)
			}
			if total-tt.floor >= topology.CPUsPerCore() {
				t.Fatalf("aggregate target %d overshoots floor %d by at least one core", total, tt.floor)
			}
		})
	}
}

func TestResolvePerNUMAReservedForReclaimRespectsUnevenNUMACapacity(t *testing.T) {
	t.Parallel()

	topology, err := GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}
	for cpuID, cpuInfo := range topology.CPUDetails {
		if cpuID == 0 || cpuID == 8 {
			cpuInfo.NUMANodeID = 2
		} else {
			cpuInfo.NUMANodeID = 7
		}
		topology.CPUDetails[cpuID] = cpuInfo
	}
	topology.NUMAToCPUs = NUMANodeInfo{
		2: topology.CPUDetails.CPUsInNUMANodes(2),
		7: topology.CPUDetails.CPUsInNUMANodes(7),
	}

	got := ResolvePerNUMAReservedForReclaim(newReclaimTestConfig(0, "10", "", ""), topology)
	want := map[int]int{2: 2, 7: 8}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolvePerNUMAReservedForReclaim() = %v, want capacity-aware %v", got, want)
	}
}

func TestResolvePerNUMAReservedForReclaimNilInputs(t *testing.T) {
	t.Parallel()

	topology, err := GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}

	// nil topology yields an empty map; the caller never dereferences a nil map.
	if got := ResolvePerNUMAReservedForReclaim(newReclaimTestConfig(0, "4", "", ""), nil); len(got) != 0 {
		t.Fatalf("ResolvePerNUMAReservedForReclaim(nil topology) = %v, want empty", got)
	}

	// nil config falls back to zero reserve, lifted to one core per NUMA node and
	// rounded up to a complete core (2 CPUs on the SMT2 dummy topology).
	got := ResolvePerNUMAReservedForReclaim(nil, topology)
	want := map[int]int{0: 2, 1: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolvePerNUMAReservedForReclaim(nil config) = %v, want %v", got, want)
	}
}

func TestResolveConfiguredReclaimFloor(t *testing.T) {
	t.Parallel()

	type args struct {
		numaReservedRatio   float64
		numaReservedFloor   int
		globalReservedCores int
		numNUMANodes        int
		cpusPerCore         int
		numaCPUSize         func(int) int
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			// ratio takes priority and sums per-NUMA ceil(ratio*size):
			// ceil(0.1*32)=4 per NUMA, ×2 = 8; the global scalar is ignored.
			// 4 is already core-aligned on SMT2 so no drift.
			name: "ratio priority sums per numa",
			args: args{
				numaReservedRatio:   0.1,
				numaReservedFloor:   2,
				globalReservedCores: 100,
				numNUMANodes:        2,
				cpusPerCore:         2,
				numaCPUSize:         evenCapacity(32),
			},
			want: 8,
		},
		{
			// floor dominates when ratio result is smaller, non-SMT zero drift:
			// ceil(0.1*10)=1 < floor 3, so 3 per NUMA, ×2 = 6.
			name: "floor dominates ratio non smt",
			args: args{
				numaReservedRatio:   0.1,
				numaReservedFloor:   3,
				globalReservedCores: 0,
				numNUMANodes:        2,
				cpusPerCore:         1,
				numaCPUSize:         evenCapacity(10),
			},
			want: 6,
		},
		{
			// SMT2: the per-NUMA floor 3 rounds UP to a complete core (4 CPUs)
			// before summing, so the scalar floor is itself core-aligned: 4×2 = 8.
			name: "smt2 rounds per numa floor up to core",
			args: args{
				numaReservedRatio:   0.1,
				numaReservedFloor:   3,
				globalReservedCores: 0,
				numNUMANodes:        2,
				cpusPerCore:         2,
				numaCPUSize:         evenCapacity(10),
			},
			want: 8,
		},
		{
			// ratio zero: fall back to the raw global reserved cores (unrounded;
			// the hard-partition distributor core-aligns the global floor later).
			name: "global fallback",
			args: args{
				numaReservedRatio:   0,
				numaReservedFloor:   5,
				globalReservedCores: 16,
				numNUMANodes:        2,
				cpusPerCore:         2,
				numaCPUSize:         evenCapacity(32),
			},
			want: 16,
		},
		{
			// ratio zero and no global reserve: no configured floor.
			name: "global fallback zero",
			args: args{
				numaReservedRatio:   0,
				numaReservedFloor:   0,
				globalReservedCores: 0,
				numNUMANodes:        2,
				cpusPerCore:         2,
				numaCPUSize:         evenCapacity(32),
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveConfiguredReclaimFloor(
				tt.args.numaReservedRatio,
				tt.args.numaReservedFloor,
				tt.args.globalReservedCores,
				tt.args.numNUMANodes,
				tt.args.cpusPerCore,
				tt.args.numaCPUSize,
			)
			if got != tt.want {
				t.Fatalf("ResolveConfiguredReclaimFloor() = %d, want %d", got, tt.want)
			}
		})
	}
}

func newReclaimTestConfig(
	ratioForRampUp float64,
	globalReserve string,
	numaReservedRatio string,
	numaReserved string,
) *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.InitialRampUpReclaimCPUSetRatio = ratioForRampUp
	if globalReserve != "" {
		conf.MinReclaimedResourceForAllocate = v1.ResourceList{v1.ResourceCPU: resource.MustParse(globalReserve)}
	}
	if numaReservedRatio != "" {
		conf.NumaMinReclaimedResourceRatioForAllocate = v1.ResourceList{v1.ResourceCPU: resource.MustParse(numaReservedRatio)}
	}
	if numaReserved != "" {
		conf.NumaMinReclaimedResourceForAllocate = v1.ResourceList{v1.ResourceCPU: resource.MustParse(numaReserved)}
	}
	return conf
}

func TestResolveConfiguredReclaimFloorFromConfig(t *testing.T) {
	t.Parallel()

	// 2 NUMA nodes, 8 CPUs each.
	topology, err := GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}

	tests := []struct {
		name                   string
		conf                   *dynamicconfig.Configuration
		globalReservedFallback int
		want                   int
	}{
		{
			// global scalar drives the floor when NUMA ratio is unset.
			name:                   "global scalar",
			conf:                   newReclaimTestConfig(0, "6", "", ""),
			globalReservedFallback: 0,
			want:                   6,
		},
		{
			// global key missing: fall back to the caller-provided default.
			name:                   "global fallback default",
			conf:                   newReclaimTestConfig(0, "", "", ""),
			globalReservedFallback: 4,
			want:                   4,
		},
		{
			// NUMA ratio takes priority: ceil(0.25*8)=2 per NUMA, ×2 = 4.
			name:                   "numa ratio priority",
			conf:                   newReclaimTestConfig(0, "100", "0.25", "1"),
			globalReservedFallback: 0,
			want:                   4,
		},
		{
			// nil config returns the fallback untouched.
			name:                   "nil config",
			conf:                   nil,
			globalReservedFallback: 7,
			want:                   7,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveConfiguredReclaimFloorFromConfig(tt.conf, topology, tt.globalReservedFallback)
			if got != tt.want {
				t.Fatalf("ResolveConfiguredReclaimFloorFromConfig() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveConfiguredReclaimFloorFromConfigUsesRealNUMAIDs(t *testing.T) {
	t.Parallel()

	topology := sparseNUMATestTopology(t)
	conf := newReclaimTestConfig(0, "0", "0.25", "0")
	if got, want := ResolveConfiguredReclaimFloorFromConfig(conf, topology, 0), 4; got != want {
		t.Fatalf("ResolveConfiguredReclaimFloorFromConfig() = %d, want %d", got, want)
	}
}

func TestResolveHardPartitionReclaimTargets(t *testing.T) {
	t.Parallel()

	// 2 NUMA nodes, 8 CPUs each.
	topology, err := GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}

	tests := []struct {
		name                    string
		conf                    *dynamicconfig.Configuration
		globalReservedFallback  int
		perNUMAReservedFloor    func(int) int
		perNUMAEligibleCapacity func(int) int
		want                    map[int]int
	}{
		{
			// ramp-up ratio 0, minimum 2 per NUMA drives the baseline (total 4);
			// global floor 4 does not raise it. targets {0:2,1:2}.
			name:                   "minimum baseline dominates",
			conf:                   newReclaimTestConfig(0, "4", "", ""),
			globalReservedFallback: 0,
			want:                   map[int]int{0: 2, 1: 2},
		},
		{
			// ramp-up ratio 0.5: floor(0.5*8)=4 per NUMA, total 8; floor 4 ignored.
			name:                   "rampup ratio baseline",
			conf:                   newReclaimTestConfig(0.5, "4", "", ""),
			globalReservedFallback: 0,
			want:                   map[int]int{0: 4, 1: 4},
		},
		{
			// global floor 10 exceeds baseline total 4; the lift adds one complete
			// core (cpusPerCore=2) at a time round-robin from {2,2}, landing on
			// {0:6,1:4} — every target whole-core, never the half-core {5,5}.
			name:                   "global floor raises by complete cores",
			conf:                   newReclaimTestConfig(0, "10", "", ""),
			globalReservedFallback: 0,
			want:                   map[int]int{0: 6, 1: 4},
		},
		{
			// per-NUMA reserved floor 3 rounds UP to a complete core on SMT2
			// (2 cores => 4 CPUs) so no half core seeds the reclaim pool; NUMA 1
			// stays at the minimum one-core baseline (2). total baseline 6.
			name:                   "per numa reserved floor rounds up to core",
			conf:                   newReclaimTestConfig(0, "0", "", ""),
			globalReservedFallback: 0,
			perNUMAReservedFloor: func(numaID int) int {
				if numaID == 0 {
					return 3
				}
				return 0
			},
			want: map[int]int{0: 4, 1: 2},
		},
		{
			// eligible capacity is smaller than full capacity because dedicated /
			// non-exclusive DNB workloads already occupy part of each NUMA. The
			// ramp-up target must be derived from the eligible core count so it
			// stays within what admission validates against. full capacity 8
			// (4 cores) would yield floor(4*0.5)=2 cores=4 CPUs, but eligible
			// capacity 4 (2 cores) yields floor(2*0.5)=1 core=2 CPUs per NUMA.
			name:                    "eligible capacity caps rampup target",
			conf:                    newReclaimTestConfig(0.5, "0", "", ""),
			globalReservedFallback:  0,
			perNUMAEligibleCapacity: func(int) int { return 4 },
			want:                    map[int]int{0: 2, 1: 2},
		},
		{
			// A NUMA fully occupied by non-exclusive DNB (or dedicated) workloads
			// has zero eligible capacity: physically no free core can be kept for
			// reclaim, so its hard-partition target is legitimately 0 rather than a
			// fail-closed error. The always-on one-core minimum baseline must not be
			// imposed on a NUMA that cannot even yield a single complete core. NUMA 0
			// has eligible 0 => target 0; NUMA 1 keeps full eligible 8 (4 cores) =>
			// floor(4*0.5)=2 cores=4 CPUs.
			name:                   "numa fully occupied yields zero target",
			conf:                   newReclaimTestConfig(0.5, "0", "", ""),
			globalReservedFallback: 0,
			perNUMAEligibleCapacity: func(numaID int) int {
				if numaID == 0 {
					return 0
				}
				return 8
			},
			want: map[int]int{0: 0, 1: 4},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveHardPartitionReclaimTargets(
				tt.conf, topology, tt.globalReservedFallback, tt.perNUMAReservedFloor, tt.perNUMAEligibleCapacity)
			if err != nil {
				t.Fatalf("ResolveHardPartitionReclaimTargets() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ResolveHardPartitionReclaimTargets() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveHardPartitionReclaimTargetsCoreAligned(t *testing.T) {
	t.Parallel()

	// SMT2 dummy topology: every per-NUMA target the resolver produces must be a
	// multiple of CPUsPerCore() across ratio, global-floor and reserved-floor
	// shapes, so a reclaim pool can always be built from complete cores.
	topology, err := GenerateDummyCPUTopology(192, 2, 4)
	if err != nil {
		t.Fatalf("generate dummy topology: %v", err)
	}
	cpusPerCore := topology.CPUsPerCore()

	confs := []*dynamicconfig.Configuration{
		newReclaimTestConfig(0.2, "0", "", ""),
		newReclaimTestConfig(0, "37", "", ""),
		newReclaimTestConfig(0, "0", "0.13", "3"),
	}
	for i, conf := range confs {
		got, err := ResolveHardPartitionReclaimTargets(conf, topology, 0, nil, nil)
		if err != nil {
			t.Fatalf("case %d: ResolveHardPartitionReclaimTargets() error = %v", i, err)
		}
		for numaID, target := range got {
			if target%cpusPerCore != 0 {
				t.Fatalf("case %d: NUMA %d target %d not a multiple of cpusPerCore %d",
					i, numaID, target, cpusPerCore)
			}
		}
	}
}
