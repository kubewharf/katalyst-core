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

package cpumetrics

import (
	"context"
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func BenchmarkCPUMetricsPlugin256Pods8NUMA(b *testing.B) {
	const (
		podCount  = 256
		numaCount = 8
	)

	pools := make(map[model.CPUSetPoolIdentity]machine.CPUSet, podCount)
	cpus := make([]int, 0, podCount)
	details := make(machine.CPUDetails, podCount)
	for cpu := 0; cpu < podCount; cpu++ {
		pools[model.CPUSetPoolIdentity{
			Kind:         model.CPUSetPoolKindDedicated,
			PodNamespace: "default",
			PodName:      fmt.Sprintf("benchmark-pod-%03d", cpu),
		}] = machine.NewCPUSet(cpu)
		cpus = append(cpus, cpu)
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: cpu % numaCount,
			SocketID:   cpu % numaCount,
			CoreID:     cpu,
		}
	}

	emitter := &captureEmitter{}
	ctx := periodicalContext(
		viewWithProjection(model.AppliedViewLevelFull, pools),
		emitter,
		newFetcherWithMetrics(completeSamples(cpus...)),
		details,
	)
	plugin := &CPUMetricsPlugin{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emitter.reset()
		if err := plugin.PeriodicalHandler(context.Background(), ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNumABuckets isolates the numaBuckets per-NUMA bucketing that used to
// fold a fresh CPUSet per CPU via repeated Union.
func BenchmarkNumABuckets(b *testing.B) {
	const (
		cpuCount  = 256
		numaCount = 8
	)
	details := make(machine.CPUDetails, cpuCount)
	allCPUs := make([]int, 0, cpuCount)
	for cpu := 0; cpu < cpuCount; cpu++ {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: cpu % numaCount, SocketID: cpu % numaCount, CoreID: cpu}
		allCPUs = append(allCPUs, cpu)
	}
	ms := metaServerWith(nil, details)
	cpus := machine.NewCPUSet(allCPUs...)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := numaBuckets(ms, cpus); len(got) != numaCount {
			b.Fatalf("numa buckets = %d, want %d", len(got), numaCount)
		}
	}
}
