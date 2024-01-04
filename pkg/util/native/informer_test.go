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

package native

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestNewPodInformer(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	informerType := reflect.TypeOf(&core.Pod{})
	SetInformerNewFunc(informerType, func(client kubernetes.Interface, _ time.Duration) cache.SharedIndexInformer {
		return cache.NewSharedIndexInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					options.LabelSelector = "aa=bb"
					return client.CoreV1().Pods("").List(context.TODO(), options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					options.LabelSelector = "aa=bb"
					return client.CoreV1().Pods("").Watch(context.TODO(), options)
				},
			},
			&core.Pod{},
			time.Hour*24,
			cache.Indexers{},
		)
	})

	podType := reflect.TypeOf(&core.Node{})
	_, ok := GetInformerNewFunc(podType)
	as.False(ok)

	f, ok := GetInformerNewFunc(informerType)
	as.True(ok)

	scheme := runtime.NewScheme()
	utilruntime.Must(core.AddToScheme(scheme))

	// get pod list by clientset
	pod1 := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod-1",
			Labels: map[string]string{
				"aa": "bb",
			},
		},
	}
	pod2 := &core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod-2",
			Labels: map[string]string{
				"aa": "cc",
			},
		},
	}
	kubeClient := fake.NewSimpleClientset()
	_, _ = kubeClient.CoreV1().Pods("").Create(context.TODO(), pod1, metav1.CreateOptions{})
	_, _ = kubeClient.CoreV1().Pods("").Create(context.TODO(), pod2, metav1.CreateOptions{})

	podItems, err := kubeClient.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	as.NoError(err)
	as.Equal(2, len(podItems.Items))

	// get pod list by shared-informer
	factoryRaw := informers.NewSharedInformerFactoryWithOptions(kubeClient, time.Hour*24)
	_ = factoryRaw.Core().V1().Pods().Informer()
	factoryRaw.Start(context.TODO().Done())

	cache.WaitForCacheSync(context.TODO().Done(), factoryRaw.Core().V1().Pods().Informer().HasSynced)
	pods, err := factoryRaw.Core().V1().Pods().Lister().List(labels.Everything())
	as.NoError(err)
	as.Equal(2, len(pods))

	// get pod list by wrapped shared-informer
	factoryWrapped := informers.NewSharedInformerFactoryWithOptions(kubeClient, time.Hour*24)
	pf := NewPodInformer(factoryWrapped.InformerFor(&core.Pod{}, f))
	factoryWrapped.Start(context.TODO().Done())

	cache.WaitForCacheSync(context.TODO().Done(), factoryWrapped.Core().V1().Pods().Informer().HasSynced)
	pods, err = pf.Lister().List(labels.Everything())
	as.NoError(err)
	as.Equal(1, len(pods))
	as.Equal("test-pod-1", pods[0].Name)
}
