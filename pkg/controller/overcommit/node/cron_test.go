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

package node

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"k8s.io/klog/v2"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestCronWorker(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		nocs      []*v1alpha1.NodeOvercommitConfig
		expectRes map[string]map[v1.ResourceName]string
	}{
		{
			name: "nil resourceOvercommitRatio",
			nocs: []*v1alpha1.NodeOvercommitConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool1",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency:         true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, 0),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "2",
											v1.ResourceMemory: "2",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool2",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency:         true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(10, 10),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "2",
											v1.ResourceMemory: "2",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-8 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool3",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						ResourceOvercommitRatio: map[v1.ResourceName]string{},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool4",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency:         true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(10, 10),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "2",
											v1.ResourceMemory: "2",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-24 * time.Hour)},
					},
				},
			},
			expectRes: map[string]map[v1.ResourceName]string{
				"pool1": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
				"pool2": {},
				"pool3": {},
				"pool4": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
			},
		},
		{
			name: "overcommit pool",
			nocs: []*v1alpha1.NodeOvercommitConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool1",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, 0),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "1.5",
											v1.ResourceMemory: "1.5",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool2",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, 0),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU: "1.5",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool3",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(2, 2),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "1.5",
											v1.ResourceMemory: "1.5",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
			},
			expectRes: map[string]map[v1.ResourceName]string{
				"pool1": {
					v1.ResourceCPU:    "1.5",
					v1.ResourceMemory: "1.5",
				},
				"pool2": {
					v1.ResourceCPU:    "1.5",
					v1.ResourceMemory: "2",
				},
				"pool3": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
			},
		},
		{
			name: "wrong data",
			nocs: []*v1alpha1.NodeOvercommitConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool1",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, 0),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											"GPU":             "xx",
											v1.ResourceCPU:    "",
											v1.ResourceMemory: "0.5",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool2",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: "***abc",
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU: "1.5",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-1 * time.Hour)},
					},
				},
			},
			expectRes: map[string]map[v1.ResourceName]string{
				"pool1": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
				"pool2": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
			},
		},
		{
			name: "history cronTab",
			nocs: []*v1alpha1.NodeOvercommitConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool1",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: true,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, -5),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceMemory: "1.5",
										},
									},
								},
							},
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, -10),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "3",
											v1.ResourceMemory: "3",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-24 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool2",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: false,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, -5),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceMemory: "1.5",
										},
									},
								},
							},
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, -10),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "3",
											v1.ResourceMemory: "3",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-24 * time.Hour)},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pool3",
					},
					Spec: v1alpha1.NodeOvercommitConfigSpec{
						CronConsistency: false,
						ResourceOvercommitRatio: map[v1.ResourceName]string{
							v1.ResourceCPU:    "2",
							v1.ResourceMemory: "2",
						},
						TimeBounds: []v1alpha1.TimeBound{
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, -5),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceMemory: "1.5",
										},
									},
								},
							},
							{
								Bounds: []v1alpha1.Bound{
									{
										CronTab: makeTestCron(0, 0),
										ResourceOvercommitRatio: map[v1.ResourceName]string{
											v1.ResourceCPU:    "3",
											v1.ResourceMemory: "3",
										},
									},
								},
							},
						},
					},
					Status: v1alpha1.NodeOvercommitConfigStatus{
						LastScheduleTime: &metav1.Time{Time: time.Now().Add(-24 * time.Hour)},
					},
				},
			},
			expectRes: map[string]map[v1.ResourceName]string{
				"pool1": {
					v1.ResourceCPU:    "3",
					v1.ResourceMemory: "1.5",
				},
				"pool2": {
					v1.ResourceCPU:    "2",
					v1.ResourceMemory: "2",
				},
				"pool3": {
					v1.ResourceCPU:    "3",
					v1.ResourceMemory: "3",
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gc, err := katalyst_base.GenerateFakeGenericContext()
			assert.NoError(t, err)

			nocConf := controller.NewOvercommitConfig()

			genericConf := &generic.GenericConfiguration{}

			nocController, err := NewNodeOvercommitController(context.TODO(), gc, genericConf, nocConf)
			assert.NoError(t, err)

			for _, noc := range tc.nocs {
				_, err = gc.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().Create(context.TODO(), noc, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			gc.StartInformer(nocController.ctx)
			synced := cache.WaitForCacheSync(context.TODO().Done(), gc.InternalInformerFactory.Overcommit().V1alpha1().NodeOvercommitConfigs().Informer().HasSynced)
			assert.True(t, synced)

			nocController.cronWorker()
			time.Sleep(time.Second)

			nocList, err := gc.Client.InternalClient.OvercommitV1alpha1().NodeOvercommitConfigs().List(context.TODO(), metav1.ListOptions{})
			assert.NoError(t, err)

			for _, noc := range nocList.Items {
				expectResourceMap, _ := tc.expectRes[noc.Name]
				klog.Infof("noc: %v expectRes: %v, specRes: %v", noc.Name, expectResourceMap, noc.Spec.ResourceOvercommitRatio)

				assert.True(t, reflect.DeepEqual(noc.Spec.ResourceOvercommitRatio, expectResourceMap))
			}
		})
	}
}

func TestTimeBoundValid(t *testing.T) {
	t.Parallel()
	now := time.Now()

	bound := v1alpha1.TimeBound{
		End: &metav1.Time{Time: now.Add(10 * time.Hour)},
	}
	assert.True(t, timeBoundValid(now, bound))

	bound = v1alpha1.TimeBound{
		Start: &metav1.Time{Time: now.Add(2 * time.Minute)},
	}
	assert.False(t, timeBoundValid(now, bound))

	bound = v1alpha1.TimeBound{
		Start: &metav1.Time{Time: now.Add(-1 * time.Hour)},
		End:   &metav1.Time{Time: now.Add(time.Hour)},
	}
	assert.True(t, timeBoundValid(now, bound))
}

func makeTestCron(m, h time.Duration) string {
	now := time.Now().Add(h * time.Hour).Add(m * time.Minute)
	hour := now.Hour()
	min := now.Minute()
	hourStr := strconv.Itoa(hour)
	minStr := strconv.Itoa(min)

	return fmt.Sprintf("%s %s * * *", minStr, hourStr)
}
