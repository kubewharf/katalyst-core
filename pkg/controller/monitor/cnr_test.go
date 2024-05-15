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

package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func TestCNRMonitor_Run(t *testing.T) {
	t.Parallel()

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod1",
			Namespace: "default",
			UID:       "uid1",
		},
		Spec: corev1.PodSpec{
			NodeName: "",
		},
	}

	type fields struct {
		pod *corev1.Pod
		cnr *v1alpha1.CustomNodeResource
	}
	tests := []struct {
		name   string
		fields fields
	}{
		{
			name: "test-pod-nodename-updated",
			fields: fields{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
					Spec: corev1.PodSpec{
						NodeName: "node1",
					},
				},
				cnr: &v1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
						Labels: map[string]string{
							"test": "test",
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{oldPod}, []runtime.Object{tt.fields.cnr})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			ctrl, err := NewCNRMonitorController(
				context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.CNRMonitorConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.KubeInformerFactory.Core().V1().Pods(),
				genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
				metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
			)
			assert.NoError(t, err)

			// test cache synced
			genericCtx.KubeInformerFactory.Start(ctrl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(ctrl.ctx.Done())
			go ctrl.Run()

			cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.podListerSynced)
			time.Sleep(100 * time.Millisecond)

			gotCNR, err := ctrl.cnrLister.Get(tt.fields.cnr.Name)
			assert.NoError(t, err)
			assert.Equal(t, tt.fields.cnr, gotCNR)

			gotPod, err := ctrl.podLister.Pods(oldPod.Namespace).Get(oldPod.Name)
			assert.NoError(t, err)
			assert.Equal(t, oldPod, gotPod)

			// test schedule pod to node
			_, err = genericCtx.Client.KubeClient.CoreV1().Pods(tt.fields.pod.Namespace).Update(ctrl.ctx, tt.fields.pod, metav1.UpdateOptions{})
			assert.NoError(t, err)
			time.Sleep(100 * time.Millisecond)
		})
	}
}

func Test_gcPodTimeMap(t *testing.T) {
	t.Parallel()

	var (
		time1 = time.Now().Add(-6 * time.Minute)
		time2 = time.Now().Add(-time.Second)
	)
	type args struct {
		pod        *corev1.Pod
		podTimeMap map[string]time.Time
	}
	tests := []struct {
		name string
		args args
		want map[string]time.Time
	}{
		{
			name: "test-gc and emit timeout cnr report lantency metric",
			args: args{
				pod: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "test",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "container-1",
								Image: "nginx:latest",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: v1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:         "container-1",
								Ready:        true,
								RestartCount: 0,
							},
						},
					},
				},
				podTimeMap: map[string]time.Time{
					"test/pod1/1": time1,
					"test/pod2/2": time2,
				},
			},
			want: map[string]time.Time{
				"test/pod2/2": time2,
			},
		},
		{
			name: "test-no-gc",
			args: args{
				podTimeMap: map[string]time.Time{
					"test/pod1/1": time2,
				},
			},
			want: map[string]time.Time{
				"test/pod1/1": time2,
			},
		},
		{
			name: "test-empty",
			args: args{
				podTimeMap: map[string]time.Time{},
			},
			want: map[string]time.Time{},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.args.pod}, []runtime.Object{})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			ctrl, err := NewCNRMonitorController(
				context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.CNRMonitorConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.KubeInformerFactory.Core().V1().Pods(),
				genericCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
				metricspool.DummyMetricsEmitterPool.GetDefaultMetricsEmitter(metricspool.DummyMetricsEmitterPool{}),
			)
			assert.NoError(t, err)

			// test cache synced
			genericCtx.KubeInformerFactory.Start(ctrl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(ctrl.ctx.Done())

			cache.WaitForCacheSync(ctrl.ctx.Done(), ctrl.cnrListerSynced, ctrl.podListerSynced)
			time.Sleep(100 * time.Millisecond)

			for k, v := range tt.args.podTimeMap {
				ctrl.podTimeMap.Store(k, v)
			}

			ctrl.gcPodTimeMap()

			podTimeMap := map[string]time.Time{}
			ctrl.podTimeMap.Range(func(k, v interface{}) bool {
				podTimeMap[k.(string)] = v.(time.Time)
				return true
			})

			assert.Equal(t, tt.want, podTimeMap)
		})
	}
}
