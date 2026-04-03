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

package userwatermark

import (
	"fmt"
	"sync"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	userwmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	katalystcoreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var managerMutex sync.Mutex

func generateUserWatermarkTestMetaServer(pods []*v1.Pod) *metaserver.MetaServer {
	podFetcher := &metapod.PodFetcherStub{PodList: pods}

	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: podFetcher,
		},
	}
}

func TestNewUserWatermarkReclaimManager(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	m := NewUserWatermarkReclaimManager(qosConfig, dynamicConf, metrics.DummyMetrics{}, (*metaserver.MetaServer)(nil))
	assert.NotNil(t, m)
	assert.Equal(t, qosConfig, m.qosConfig)
	assert.Equal(t, dynamicConf, m.dynamicConf)
	assert.NotNil(t, m.started)
	assert.NotNil(t, m.containerReclaimer)
	assert.NotNil(t, m.cgroupPathReclaimer)
}

func TestUserWatermarkReclaimManager_Reconcile_Disabled(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	conf := dynamicConf.GetDynamicConfiguration()
	conf.UserWatermarkConfiguration.EnableReclaimer = false

	m := NewUserWatermarkReclaimManager(qosConfig, dynamicConf, metrics.DummyMetrics{}, (*metaserver.MetaServer)(nil))

	// pre-populate started/container maps to ensure they are not touched when disabled
	m.started["test"] = true
	m.containerReclaimer[katalystcoreconsts.PodContainerName("pod,container")] = &userWatermarkReclaimer{}

	m.reconcile()
	assert.True(t, m.started["test"])
	_, exist := m.containerReclaimer[katalystcoreconsts.PodContainerName("pod,container")]
	assert.True(t, exist)
}

func TestUserWatermarkReclaimManager_Reconcile_CreateReclaimers(t *testing.T) {
	t.Parallel()
	managerMutex.Lock()
	defer managerMutex.Unlock()

	defer mockey.UnPatchAll()
	qosConfig := generic.NewQoSConfiguration()

	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	conf := dynamicConf.GetDynamicConfiguration()
	conf.UserWatermarkConfiguration.EnableReclaimer = true
	conf.UserWatermarkConfiguration.ServiceLabel = "svc-label"

	// add a cgroup level configuration
	cgPath := "/sys/fs/cgroup/memory/custom"
	conf.UserWatermarkConfiguration.CgroupConfig[cgPath] = userwmconfig.NewReclaimConfigDetail(conf.UserWatermarkConfiguration.DefaultConfig)

	podObj := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "pod-uid-1",
			Name:      "test-pod",
			Namespace: "default",
			Annotations: map[string]string{
				katalystapiconsts.PodAnnotationQoSLevelKey: katalystapiconsts.PodAnnotationQoSLevelSharedCores,
			},
			Labels: map[string]string{
				"svc-label": "svc-a",
			},
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:        "c1",
					ContainerID: "containerd://cid-1",
					Ready:       true,
				},
			},
		},
	}

	ms := generateUserWatermarkTestMetaServer([]*v1.Pod{podObj})

	// patch container cgroup path helper to avoid touching real cgroups
	mockey.Mock(common.GetContainerAbsCgroupPath).
		To(func(_ string, podUID, containerID string) (string, error) {
			return "/sys/fs/cgroup/memory/test-cgroup", nil
		}).Build()

	m := NewUserWatermarkReclaimManager(qosConfig, dynamicConf, metrics.DummyMetrics{}, ms)
	m.reconcile()

	// container based reclaimer
	podContainerName := native.GeneratePodContainerName(podObj.Name, podObj.Status.ContainerStatuses[0].Name)
	_, exist := m.containerReclaimer[podContainerName]
	assert.True(t, exist)
	assert.True(t, m.started[string(podContainerName)])

	// cgroup based reclaimer
	_, exist = m.cgroupPathReclaimer[cgPath]
	assert.True(t, exist)
	assert.True(t, m.started[cgPath])
}

func TestUserWatermarkReclaimManager_Reconcile_GetPodListError(t *testing.T) {
	t.Parallel()
	managerMutex.Lock()
	defer managerMutex.Unlock()

	defer mockey.UnPatchAll()

	qosConfig := generic.NewQoSConfiguration()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	conf := dynamicConf.GetDynamicConfiguration()
	conf.UserWatermarkConfiguration.EnableReclaimer = true

	// meta server with stub pod fetcher
	pods := []*v1.Pod{{}}
	ms := generateUserWatermarkTestMetaServer(pods)

	// force PodFetcherStub.GetPodList to return error
	mockErr := fmt.Errorf("list error")
	mockey.Mock((*metapod.PodFetcherStub).GetPodList).Return(nil, mockErr).Build()

	m := NewUserWatermarkReclaimManager(qosConfig, dynamicConf, metrics.DummyMetrics{}, ms)
	m.reconcile()

	// should early return and create no reclaimers
	assert.Empty(t, m.containerReclaimer)
	assert.Empty(t, m.cgroupPathReclaimer)
}

type fakeReclaimer struct {
	stopped bool
}

func (f *fakeReclaimer) Run()                                         {}
func (f *fakeReclaimer) Stop()                                        { f.stopped = true }
func (f *fakeReclaimer) LoadConfig()                                  {}
func (f *fakeReclaimer) GetConfig() *userwmconfig.ReclaimConfigDetail { return nil }

func TestUserWatermarkReclaimManager_Reconcile_CleanupStaleReclaimers(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
	conf := dynamicConf.GetDynamicConfiguration()
	conf.UserWatermarkConfiguration.EnableReclaimer = true

	// meta server with no running pods
	ms := generateUserWatermarkTestMetaServer(nil)

	m := NewUserWatermarkReclaimManager(qosConfig, dynamicConf, metrics.DummyMetrics{}, ms)

	podContainerKey := katalystcoreconsts.PodContainerName("pod-1,container-1")
	cgPath := "/sys/fs/cgroup/memory/old"

	frContainer := &fakeReclaimer{}
	frCgroup := &fakeReclaimer{}

	m.containerReclaimer[podContainerKey] = frContainer
	m.cgroupPathReclaimer[cgPath] = frCgroup

	m.started[string(podContainerKey)] = true
	m.started[cgPath] = true

	// no CgroupConfig for cgPath, and no pods => both reclaimers should be cleaned
	m.reconcile()

	_, exist := m.containerReclaimer[podContainerKey]
	assert.False(t, exist)

	_, exist = m.cgroupPathReclaimer[cgPath]
	assert.False(t, exist)

	_, exist = m.started[string(podContainerKey)]
	assert.False(t, exist)

	_, exist = m.started[cgPath]
	assert.False(t, exist)

	assert.True(t, frContainer.stopped)
	assert.True(t, frCgroup.stopped)
}
