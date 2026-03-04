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

package dynamicpolicy

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetIRQForbiddenCores(t *testing.T) {
	t.Parallel()

	tmpDir, err := ioutil.TempDir("", "checkpoint-TestGetIRQForbiddenCores")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 2, 2)
	require.NoError(t, err)

	policy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	require.NoError(t, err)

	// Mock reserved CPUs
	policy.reservedCPUs = machine.NewCPUSet(0, 1)

	// Prepare resource packages in NPD
	npdFetcher := &npd.DummyNPDFetcher{
		NPD: &nodev1alpha1.NodeProfileDescriptor{
			Status: nodev1alpha1.NodeProfileDescriptorStatus{
				NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
					{
						Scope: "resource-package",
						Metrics: []nodev1alpha1.MetricValue{
							{
								MetricName: string(v1.ResourceCPU),
								MetricLabels: map[string]string{
									"package-name":  "pkg1",
									"numa-id":       "0",
									"pinned-cpuset": "true",
									"type":          "forbidden",
								},
								Value:      *resource.NewQuantity(2, resource.DecimalSI),
								Aggregator: func() *nodev1alpha1.Aggregator { a := nodev1alpha1.AggregatorMin; return &a }(),
							},
							{
								MetricName: string(v1.ResourceCPU),
								MetricLabels: map[string]string{
									"package-name":  "pkg2",
									"numa-id":       "1",
									"pinned-cpuset": "true",
									"type":          "allowed",
								},
								Value:      *resource.NewQuantity(2, resource.DecimalSI),
								Aggregator: func() *nodev1alpha1.Aggregator { a := nodev1alpha1.AggregatorMin; return &a }(),
							},
						},
					},
				},
			},
		},
	}
	policy.resourcePackageManager = resourcepackage.NewCachedResourcePackageManager(resourcepackage.NewResourcePackageManager(npdFetcher))
	stopCh := make(chan struct{})
	defer close(stopCh)
	// Run cached manager to populate cache
	go policy.resourcePackageManager.Run(stopCh)
	time.Sleep(100 * time.Millisecond)

	// Mock machine state to include pinned CPUs for packages
	// Note: In a real scenario, this state is populated by policy logic.
	// Here we need to manually inject it into the state if we want GetAggResourcePackagePinnedCPUSet to find it.
	// However, GetAggResourcePackagePinnedCPUSet reads from policy.state.GetMachineState().
	// We need to update the machine state with ResourcePackagePinnedCPUSet.

	// Assuming NUMA 0 has pkg1 pinned to CPUs 2, 3
	// Assuming NUMA 1 has pkg2 pinned to CPUs 4, 5
	machineState := policy.state.GetMachineState()
	machineState[0].ResourcePackagePinnedCPUSet = map[string]machine.CPUSet{
		"pkg1": machine.NewCPUSet(2, 3),
	}
	machineState[1].ResourcePackagePinnedCPUSet = map[string]machine.CPUSet{
		"pkg2": machine.NewCPUSet(4, 5),
	}
	policy.state.SetMachineState(machineState, false)

	// Configure attribute selector
	selector, err := labels.Parse("type=forbidden")
	require.NoError(t, err)
	policy.conf.IRQForbiddenPinnedResourcePackageAttributeSelector = selector

	// Run the test
	forbiddenCores, err := policy.GetIRQForbiddenCores()
	require.NoError(t, err)

	// Expected: Reserved CPUs (0, 1) + Pinned CPUs for pkg1 (2, 3) = (0, 1, 2, 3)
	// pkg2 is excluded because type=allowed != type=forbidden
	expected := machine.NewCPUSet(0, 1, 2, 3)
	assert.True(t, expected.Equals(forbiddenCores), "expected %v, got %v", expected, forbiddenCores)
}

func BenchmarkGetIRQForbiddenCores(b *testing.B) {
	tmpDir, err := ioutil.TempDir("", "checkpoint-BenchmarkGetIRQForbiddenCores")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	if err != nil {
		b.Fatal(err)
	}

	policy, err := getTestDynamicPolicyWithoutInitialization(cpuTopology, tmpDir)
	if err != nil {
		b.Fatal(err)
	}

	policy.reservedCPUs = machine.NewCPUSet(0, 1)

	// Prepare resource packages
	npdFetcher := &npd.DummyNPDFetcher{
		NPD: &nodev1alpha1.NodeProfileDescriptor{
			Status: nodev1alpha1.NodeProfileDescriptorStatus{
				NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
					{
						Scope: "resource-package",
						Metrics: []nodev1alpha1.MetricValue{
							{
								MetricName: string(v1.ResourceCPU),
								MetricLabels: map[string]string{
									"package-name":  "pkg1",
									"numa-id":       "0",
									"pinned-cpuset": "true",
									"type":          "forbidden",
								},
								Value:      *resource.NewQuantity(2, resource.DecimalSI),
								Aggregator: func() *nodev1alpha1.Aggregator { a := nodev1alpha1.AggregatorMin; return &a }(),
							},
						},
					},
				},
			},
		},
	}
	policy.resourcePackageManager = resourcepackage.NewCachedResourcePackageManager(resourcepackage.NewResourcePackageManager(npdFetcher))
	stopCh := make(chan struct{})
	defer close(stopCh)
	go policy.resourcePackageManager.Run(stopCh)

	machineState := policy.state.GetMachineState()
	machineState[0].ResourcePackagePinnedCPUSet = map[string]machine.CPUSet{
		"pkg1": machine.NewCPUSet(2, 3),
	}
	policy.state.SetMachineState(machineState, false)

	selector, _ := labels.Parse("type=forbidden")
	policy.conf.IRQForbiddenPinnedResourcePackageAttributeSelector = selector

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = policy.GetIRQForbiddenCores()
	}
}
