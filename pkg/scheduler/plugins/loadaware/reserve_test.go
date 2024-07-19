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

package loadaware

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cache2 "k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
)

func TestReserve(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		args:         makeTestArgs(),
		spdLister:    controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister(),
		spdHasSynced: controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced,
		cache: &Cache{
			NodePodInfo: map[string]*NodeCache{},
		},
	}
	p.cache.SetSPDLister(p)

	testPod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Name:      "testPod",
			Namespace: "default",
			OwnerReferences: []v12.OwnerReference{
				{
					Name: "reserveDeployment1",
					Kind: "Deployment",
				},
			},
		},
	}
	testNode := "testReserveNode"
	testSPD := &v1alpha1.ServiceProfileDescriptor{
		ObjectMeta: v12.ObjectMeta{
			Name:      "reserveDeployment1",
			Namespace: "default",
		},
		Status: v1alpha1.ServiceProfileDescriptorStatus{
			AggMetrics: []v1alpha1.AggPodMetrics{
				{
					Scope: spdPortraitScope,
					Items: fixedItems(4, 8*1024*1024*1024),
				},
			},
		},
	}
	_, err = controlCtx.Client.InternalClient.WorkloadV1alpha1().ServiceProfileDescriptors(testSPD.GetNamespace()).
		Create(context.TODO(), testSPD, v12.CreateOptions{})
	assert.NoError(t, err)
	controlCtx.StartInformer(context.TODO())
	// wait for portrait synced
	if !cache2.WaitForCacheSync(context.TODO().Done(), p.spdHasSynced) {
		t.Error("wait for portrait informer synced fail")
		t.FailNow()
	}

	_ = p.Reserve(context.TODO(), nil, testPod, testNode)
	resourceUsage := p.cache.GetNodePredictUsage(testNode)
	assert.Equal(t, portraitItemsLength, len(resourceUsage.Cpu))
	assert.Equal(t, portraitItemsLength, len(resourceUsage.Memory))
	assert.NotZero(t, resourceUsage.Cpu[0])

	p.Unreserve(context.TODO(), nil, testPod, testNode)
	resourceUsage = p.cache.GetNodePredictUsage(testNode)
	assert.Zero(t, resourceUsage.Cpu[0])
}
