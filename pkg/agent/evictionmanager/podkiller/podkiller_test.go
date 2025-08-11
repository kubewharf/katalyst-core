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

package podkiller

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"testing"

	"github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

func TestPodKeyFunc(t *testing.T) {
	t.Parallel()
	Convey("TestPodKeyFunc", t, func() {
		key := podKeyFunc("namespace", "name", "uid")
		So(key, ShouldEqual, "namespace/name/uid")
	})
}

func TestEvictionKeyFunc(t *testing.T) {
	t.Parallel()
	Convey("TestEvictionKeyFunc", t, func() {
		key := evictionKeyFunc("podKey", 10)
		So(key, ShouldEqual, "podKey/10")
	})
}

func TestSplitEvictionKey(t *testing.T) {
	t.Parallel()
	Convey("TestSplitEvictionKey", t, func() {
		Convey("ValidKey", func() {
			key := "part1/part2/part3/12345"
			p1, p2, p3, gp, err := splitEvictionKey(key)
			So(err, ShouldBeNil)
			So(p1, ShouldEqual, "part1")
			So(p2, ShouldEqual, "part2")
			So(p3, ShouldEqual, "part3")
			So(gp, ShouldEqual, 12345)
		})

		Convey("InvalidKeyFormat", func() {
			key := "part1/part2"
			_, _, _, _, err := splitEvictionKey(key)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "unexpected key format: "+key)
		})

		Convey("InvalidGracePeriod", func() {
			key := "part1/part2/part3/not-a-number"
			_, _, _, _, err := splitEvictionKey(key)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, "unexpected gracePeriodSeconds: not-a-number")
		})
	})
}

// mock implementations for interfaces

type mockKiller struct {
	EvictFunc func(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error
}

func (m *mockKiller) Name() string {
	return "mock-killer"
}

func (m *mockKiller) Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	if m.EvictFunc != nil {
		return m.EvictFunc(ctx, pod, gracePeriodSeconds, reason, plugin)
	}
	return nil
}

// TestAsynchronizedPodKiller_sync tests the sync method of AsynchronizedPodKiller
func TestAsynchronizedPodKiller_sync(t *testing.T) {
	t.Parallel()
	mockey.PatchConvey("When pod is successfully evicted", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		testPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{PodList: []*v1.Pod{testPod}},
			client:         fake.NewSimpleClientset(testPod),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		// Add pod to processingPods
		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      name,
					UID:       uid,
				},
			},
			Reason: reason,
			Plugin: plugin,
		}

		// Mock killer.Evict to return nil (success)
		mockKiller.EvictFunc = func(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
			return nil
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)

		// Assert
		So(err, ShouldBeNil)
		So(requeue, ShouldBeFalse)
		// Check that the pod is removed from processingPods
		So(killer.processingPods[podKey], ShouldBeNil)
	})

	mockey.PatchConvey("When pod is not found", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{},
			client:         fake.NewSimpleClientset(),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		// Add pod to processingPods
		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      name,
					UID:       uid,
				},
			},
			Reason: reason,
			Plugin: plugin,
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)

		// Assert
		So(err, ShouldBeNil)
		So(requeue, ShouldBeFalse)
		// Check that the pod is removed from processingPods
		So(killer.processingPods[podKey], ShouldBeNil)
	})

	mockey.PatchConvey("When evict pod is not found in processingPods", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid")
		gracePeriodSeconds := int64(30)

		testPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{PodList: []*v1.Pod{testPod}},
			client:         fake.NewSimpleClientset(testPod),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		// Don't add pod to processingPods to simulate the error condition
		podKey := podKeyFunc(namespace, name, string(uid))

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)

		// Assert
		So(err, ShouldNotBeNil)
		So(requeue, ShouldBeFalse)
		// Check error message
		So(err.Error(), ShouldContainSubstring, "evict pod can't be found")
	})

	mockey.PatchConvey("When there is an error during eviction", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		testPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{PodList: []*v1.Pod{testPod}},
			client:         fake.NewSimpleClientset(testPod),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		// Add pod to processingPods
		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      name,
					UID:       uid,
				},
			},
			Reason: reason,
			Plugin: plugin,
		}

		// Mock killer.Evict to return an error
		evictionError := fmt.Errorf("eviction failed")
		mockKiller.EvictFunc = func(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
			return evictionError
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)

		// Assert
		So(err, ShouldNotBeNil)
		So(err, ShouldEqual, evictionError)
		So(requeue, ShouldBeTrue)
		// Check that the pod is still in processingPods (because of requeue)
		So(killer.processingPods[podKey][gracePeriodSeconds], ShouldNotBeNil)
	})

	mockey.PatchConvey("When eviction key is invalid", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{},
			client:         fake.NewSimpleClientset(),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		// Use an invalid key
		invalidKey := "invalid-key"

		// Act
		err, requeue := killer.sync(invalidKey)

		// Assert
		So(err, ShouldNotBeNil)
		So(requeue, ShouldBeFalse)
		// Check error message
		So(err.Error(), ShouldContainSubstring, "invalid resource key")
	})

	mockey.PatchConvey("When there is a pod with the same name as the pod that we have evicted", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid1 := types.UID("test-uid1")
		uid2 := types.UID("test-uid2")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		// Have 2 pods with the same name but different UIDs
		testPod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid1,
			},
		}

		testPod2 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid2,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{PodList: []*v1.Pod{testPod2}},
			client:         fake.NewSimpleClientset(testPod2),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		podKey := podKeyFunc(namespace, name, string(uid1))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod:    testPod1,
			Reason: reason,
			Plugin: plugin,
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)
		// Should have identified that old pod is already evicted and no requeue
		So(err, ShouldBeNil)
		So(requeue, ShouldBeFalse)
		// Old pod should be removed from cache
		So(killer.processingPods[podKey], ShouldBeNil)
	})

	mockey.PatchConvey("When podFetcher throws an error but api server identifies the pod and successfully evicts it", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid1")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		testPod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{},
			client:         fake.NewSimpleClientset(testPod1),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod:    testPod1,
			Reason: reason,
			Plugin: plugin,
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Mock killer.Evict to return nil (success)
		mockKiller.EvictFunc = func(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
			return nil
		}

		// Act
		err, requeue := killer.sync(evictionKey)
		So(err, ShouldBeNil)

		So(requeue, ShouldBeFalse)
		So(killer.processingPods[podKey], ShouldBeNil)
	})

	mockey.PatchConvey("When podFetcher throws an error and api server does not successfully evict the pod", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid1")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		testPod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{},
			client:         fake.NewSimpleClientset(testPod1),
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod:    testPod1,
			Reason: reason,
			Plugin: plugin,
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Mock killer.Evict to return an error
		evictionError := fmt.Errorf("eviction failed")
		mockKiller.EvictFunc = func(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
			return evictionError
		}

		// Act
		err, requeue := killer.sync(evictionKey)
		So(err, ShouldNotBeNil)
		So(err, ShouldEqual, evictionError)

		So(requeue, ShouldBeTrue)
		So(killer.processingPods[podKey], ShouldNotBeNil)
	})

	mockey.PatchConvey("When API server throws an error unexpectedly", t, func() {
		// Arrange
		mockKiller := &mockKiller{}

		// Prepare test data
		namespace := "default"
		name := "test-pod"
		uid := types.UID("test-uid1")
		gracePeriodSeconds := int64(30)
		reason := "test-reason"
		plugin := "test-plugin"

		testPod1 := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
				UID:       uid,
			},
		}

		fakeClient := fake.NewSimpleClientset()

		evictionError := fmt.Errorf("error when getting pod")
		// Inject error when trying to get any pod
		fakeClient.Fake.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, evictionError
		})

		// Create an AsynchronizedPodKiller instance
		killer := &AsynchronizedPodKiller{
			killer:         mockKiller,
			podFetcher:     &pod.PodFetcherStub{},
			client:         fakeClient,
			processingPods: make(map[string]map[int64]*evictPodInfo),
		}

		podKey := podKeyFunc(namespace, name, string(uid))
		killer.processingPods[podKey] = make(map[int64]*evictPodInfo)
		killer.processingPods[podKey][gracePeriodSeconds] = &evictPodInfo{
			Pod:    testPod1,
			Reason: reason,
			Plugin: plugin,
		}

		// Create eviction key
		evictionKey := fmt.Sprintf("%s%s%d", podKey, consts.KeySeparator, gracePeriodSeconds)

		// Act
		err, requeue := killer.sync(evictionKey)
		So(err, ShouldNotBeNil)
		So(err, ShouldEqual, evictionError)

		So(requeue, ShouldBeTrue)
		So(killer.processingPods[podKey], ShouldNotBeNil)
	})
}
