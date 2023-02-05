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

package rule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
)

func makeRuledEvictPodWithAnnotation(name, scope string, annotations map[string]string) *RuledEvictPod {
	ep := makeRuledEvictPod(name, scope)
	ep.Pod.Annotations = annotations
	return ep
}

func makeRuledEvictPodForSort(name, scope string, annotations map[string]string, priority int32) *RuledEvictPod {
	ep := makeRuledEvictPodWithAnnotation(name, scope, annotations)
	ep.Pod.Spec.Priority = &priority
	return ep
}

func TestEvictionStrategyImp(t *testing.T) {
	testConf, _ := options.NewOptions().Config()
	s := NewEvictionStrategyImpl(testConf)

	for _, tc := range []struct {
		comment  string
		ep       *RuledEvictPod
		expected bool
	}{
		{
			comment: "system qos should be skipped",
			ep: makeRuledEvictPodWithAnnotation("system-p", "", map[string]string{
				apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSystemCores,
			}),
			expected: false,
		},
		{
			comment: "critical pod should be skipped",
			ep: makeRuledEvictPodWithAnnotation("system-p", "", map[string]string{
				kubelettypes.ConfigSourceAnnotationKey: "test",
			}),
			expected: false,
		},
		{
			comment:  "normal pod should  not be skipped",
			ep:       makeRuledEvictPodWithAnnotation("system-p", "", map[string]string{}),
			expected: true,
		},
	} {
		assert.Equal(t, tc.expected, s.CandidateValidate(tc.ep))
	}

	rpList := RuledEvictPodList{
		makeRuledEvictPodForSort("p-shared-priority-60-emp", "emp", map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 60),
		makeRuledEvictPodForSort("p-dedicated-priority-60-memory", EvictionScopeMemory, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		}, 60),
		makeRuledEvictPodForSort("p-shared-priority-80-force", EvictionScopeForce, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 80),
		makeRuledEvictPodForSort("p-dedicated-priority-60-emp", "emp", map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		}, 60),
		makeRuledEvictPodForSort("p-reclaimed-priority-100-force", EvictionScopeForce, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		}, 100),
		makeRuledEvictPodForSort("p-reclaimed-priority-20-force", EvictionScopeForce, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		}, 20),
		makeRuledEvictPodForSort("p-shared-priority-70-memory", EvictionScopeMemory, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 70),
		makeRuledEvictPodForSort("p-shared-priority-100-force", EvictionScopeForce, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 100),
		makeRuledEvictPodForSort("p-dedicated-priority-80-memory", EvictionScopeMemory, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
		}, 80),
		makeRuledEvictPodForSort("p-shared-priority-60-cpu", EvictionScopeCPU, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 60),
		makeRuledEvictPodForSort("p-reclaimed-priority-100-emp", "emp", map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		}, 100),
		makeRuledEvictPodForSort("p-shared-priority-80-cpu", EvictionScopeCPU, map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
		}, 80),
	}
	s.CandidateSort(rpList)

	assert.Equal(t, []string{
		"p-shared-priority-100-force",
		"p-shared-priority-80-force",
		"p-dedicated-priority-80-memory",
		"p-shared-priority-80-cpu",
		"p-shared-priority-70-memory",
		"p-dedicated-priority-60-memory",
		"p-shared-priority-60-cpu",
		"p-shared-priority-60-emp",
		"p-dedicated-priority-60-emp",
		"p-reclaimed-priority-100-force",
		"p-reclaimed-priority-100-emp",
		"p-reclaimed-priority-20-force",
	}, rpList.getPodNames())
}
