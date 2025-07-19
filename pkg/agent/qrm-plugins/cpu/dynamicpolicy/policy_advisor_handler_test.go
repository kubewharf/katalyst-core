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
	"encoding/json"
	"os"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	resource2 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/v1/resource"
)

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	t.Parallel()

	policy := &DynamicPolicy{}

	t.Run("basic path", func(t *testing.T) {
		t.Parallel()

		_ = mockey.Mock(os.ReadDir).IncludeCurrentGoRoutine().To(func(dirname string) ([]os.DirEntry, error) {
			return []os.DirEntry{
				mockDirEntry{"foo", true},
				mockDirEntry{"bar", true},
			}, nil
		}).Build()

		dirs, err := policy.getAllDirs("/fake/path")
		assert.NoError(t, err)
		assert.ElementsMatch(t, dirs, []string{"foo", "bar"})
	})
}

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() os.FileMode          { return 0 }
func (m mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestDynamicPolicy_applyCgroupConfigs(t *testing.T) {
	t.Parallel()

	resources := &common.CgroupResources{
		CpuQuota:  1000,
		CpuPeriod: 1000,
	}

	mockBytes, _ := json.Marshal(resources)

	mockResp := &advisorapi.ListAndWatchResponse{
		ExtraEntries: []*advisorsvc.CalculationInfo{
			{
				CgroupPath: "test_cgroup_path",
				CalculationResult: &advisorsvc.CalculationResult{
					Values: map[string]string{
						string(advisorapi.ControlKnobKeyCgroupConfig): string(mockBytes),
					},
				},
			},
		},
	}

	mockPodPathMap := map[string]*v1.Pod{
		"test-pod-1": {
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "test-container",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource2.MustParse("2"),
							},
						},
					},
				},
			},
		},
	}

	cpuResourceList := v1.ResourceList{
		v1.ResourceCPU: resource2.MustParse("2"),
	}

	p := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	mockey.PatchConvey("When calculating cgroup configs ", t, func() {
		mockey.Mock(general.IsPathExists).IncludeCurrentGoRoutine().Return(true).Build()
		mockey.Mock(common.CheckCgroup2UnifiedMode).IncludeCurrentGoRoutine().Return(false).Build()
		mockey.Mock((*DynamicPolicy).getAllPodsPathMap).IncludeCurrentGoRoutine().Return(mockPodPathMap, nil).Build()
		mockey.Mock(common.GetAbsCgroupPath).IncludeCurrentGoRoutine().Return("test-pod-1").Build()
		mockey.Mock((*DynamicPolicy).getAllDirs).IncludeCurrentGoRoutine().Return([]string{"test-pod-1"}, nil).Build()
		mockey.Mock((*DynamicPolicy).checkAllContainersQuota).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock((*DynamicPolicy).applyCPUQuotaWithRelativePath).IncludeCurrentGoRoutine().Return(nil).Build()
		mockey.Mock(resource.PodRequestsAndLimits).IncludeCurrentGoRoutine().Return(cpuResourceList, cpuResourceList).Build()
		mockey.Mock(common.ApplyCgroupConfigs).IncludeCurrentGoRoutine().Return(nil).When(func(path string, resources *common.CgroupResources) bool { return path == "test_cgroup_path" }).Build()

		err := p.applyCgroupConfigs(mockResp)

		convey.So(err, convey.ShouldBeNil)
	})
}
