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

package plugin

import (
	"io/ioutil"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func generateTestMemoryAdvisorConfiguration(t *testing.T) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	tmpMemoryAdvisorSocketDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir
	conf.QRMAdvisorConfiguration.MemoryAdvisorSocketAbsPath = tmpMemoryAdvisorSocketDir + "-memory_advisor.sock"
	conf.QRMAdvisorConfiguration.MemoryPluginSocketAbsPath = tmpMemoryAdvisorSocketDir + "-memory_plugin.sock"

	return conf
}

func getFakeCgroupPath(podUID, containerID string) string {
	return podUID + "/" + containerID
}

func generatePod(uid, containerName, containerID string, limits v1.ResourceList) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID: apitypes.UID(uid),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: containerName,
					Resources: v1.ResourceRequirements{
						Limits: limits,
					},
				},
			},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        containerName,
					ContainerID: containerID,
				},
			},
		},
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		podUID           string
		containerID      string
		containerName    string
		memoryUpperLimit resource.Quantity
		isReclaimedPod   bool
	}{
		{
			name:             "reclaimed_pod_has_memory_limit",
			podUID:           "pod1",
			containerID:      "container1",
			containerName:    "container1",
			memoryUpperLimit: resource.MustParse("100"),
			isReclaimedPod:   true,
		},
		{
			name:             "shared_pod_has_memory_limit",
			podUID:           "pod2",
			containerID:      "container2",
			containerName:    "container2",
			memoryUpperLimit: resource.MustParse("100"),
			isReclaimedPod:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestMemoryAdvisorConfiguration(t)
			metricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
			metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metricsFetcher)
			assert.NoError(t, err)

			fetcher := pod.PodFetcherStub{}
			metaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &fetcher,
				},
			}

			ml := NewMemoryLimit(nil, nil, metaCache, metaServer, nil)
			ml.(*memoryLimit).getCgroupPathFunc = func(podUID string, containerID string) (string, error) {
				return getFakeCgroupPath(podUID, containerID), nil
			}
			containerInfo := types.ContainerInfo{
				PodUID:        tc.podUID,
				ContainerName: tc.containerName,
			}
			if tc.isReclaimedPod {
				containerInfo.QoSLevel = apiconsts.PodAnnotationQoSLevelReclaimedCores
			} else {
				containerInfo.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
			}

			err = metaCache.SetContainerInfo(tc.podUID, tc.containerName, &containerInfo)
			assert.NoError(t, err)

			testPod := generatePod(tc.podUID, tc.containerName, tc.containerID, v1.ResourceList{
				v1.ResourceMemory: tc.memoryUpperLimit,
			})
			fetcher.PodList = append(fetcher.PodList, testPod)

			err = ml.Reconcile(nil)
			assert.NoError(t, err)

			advices := ml.GetAdvices()
			found := false
			for _, entry := range advices.ExtraEntries {
				if strings.Contains(entry.CgroupPath, tc.podUID) && strings.Contains(entry.CgroupPath, tc.containerID) && tc.isReclaimedPod {
					assert.Equal(t, entry.Values[string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes)], tc.memoryUpperLimit.String())
					found = true
				}
			}
			if !found && tc.isReclaimedPod {
				t.Errorf("Expected to find memory limit for reclaimed pod %s", tc.podUID)
			}
			if found && !tc.isReclaimedPod {
				t.Errorf("Unexpected to find memory limit for shared pod %s", tc.podUID)
			}
		})
	}
}
