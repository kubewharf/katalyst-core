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

package cni

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type updaterFetcherFunc func(context.Context) (CNIInfo, error)

func (f updaterFetcherFunc) FetchCNIInfo(ctx context.Context) (CNIInfo, error) { return f(ctx) }

func newTestUpdater(t *testing.T, fetcher CNIFetcher, client kubernetes.Interface) *CNIPeriodicNodeUpdater {
	t.Helper()
	u, err := NewCNIPeriodicNodeUpdater(fetcher, client, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	u.healthCheckName = t.Name()
	general.RegisterTemporaryHeartbeatCheck(u.healthCheckName, 0, general.HealthzCheckStateNotReady, 0)
	t.Cleanup(func() { general.UnregisterTemporaryHeartbeatCheck(u.healthCheckName) })
	return u
}

func TestPeriodicNodeUpdaterDependencies(t *testing.T) {
	t.Parallel()
	fetcher := updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{}, nil })
	for _, tc := range []struct {
		name     string
		fetcher  CNIFetcher
		client   kubernetes.Interface
		nodeName string
	}{
		{"nil fetcher", nil, fake.NewSimpleClientset(), "node-1"},
		{"nil client", fetcher, nil, "node-1"},
		{"empty node name", fetcher, fake.NewSimpleClientset(), ""},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewCNIPeriodicNodeUpdater(tc.fetcher, tc.client, tc.nodeName); err == nil {
				t.Fatal("invalid dependencies accepted")
			}
		})
	}
}

type countingKubeClient struct {
	kubernetes.Interface
	coreV1Calls atomic.Int64
}

func (c *countingKubeClient) CoreV1() corev1.CoreV1Interface {
	c.coreV1Calls.Add(1)
	return c.Interface.CoreV1()
}

func TestGetCNIPeriodicNodeUpdater(t *testing.T) {
	t.Parallel()
	apiClient := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	client := &countingKubeClient{Interface: apiClient}
	first := updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{AllocatableDirectIPs: 200}, nil })
	for _, tc := range []struct {
		fetcher  CNIFetcher
		client   kubernetes.Interface
		nodeName string
	}{{nil, client, "node-1"}, {first, nil, "node-1"}, {first, client, ""}} {
		if u, err := GetCNIPeriodicNodeUpdater(tc.fetcher, tc.client, tc.nodeName); u != nil || err == nil {
			t.Fatal("invalid call accepted")
		}
	}
	results := make(chan *CNIPeriodicNodeUpdater, 16)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := GetCNIPeriodicNodeUpdater(first, client, "node-1")
			if err != nil {
				t.Error(err)
				return
			}
			results <- u
		}()
	}
	wg.Wait()
	close(results)
	u, err := GetCNIPeriodicNodeUpdater(updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{AllocatableDirectIPs: 999}, nil }), fake.NewSimpleClientset(), "other-node")
	if err != nil {
		t.Fatal(err)
	}
	for other := range results {
		if other != u {
			t.Fatal("getter returned different instances")
		}
	}
	if calls := client.coreV1Calls.Load(); calls != 1 {
		t.Fatalf("singleton constructed %d times, want 1", calls)
	}
	if len(apiClient.Actions()) != 0 {
		t.Fatal("constructor accessed the API")
	}
	if initial := u.GetSnapshot(); initial.Info != (CNIInfo{}) || initial.Err == nil {
		t.Fatalf("bad initial snapshot: %+v", initial)
	}
	general.RegisterTemporaryHeartbeatCheck(NodeUpdaterHandlerName, 0, general.HealthzCheckStateNotReady, 0)
	t.Cleanup(func() { general.UnregisterTemporaryHeartbeatCheck(NodeUpdaterHandlerName) })
	u.Refresh(nil, nil, nil, nil, nil)
	if got := u.GetSnapshot(); got.Info.AllocatableDirectIPs != 200 || got.Err != nil {
		t.Fatalf("first fetcher replaced: %+v", got)
	}
	node, err := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quantity := node.Status.Capacity[consts.ResourcePodIPs]
	if quantity.Value() != 200 {
		t.Fatal("first client or node name replaced")
	}
}

func TestPeriodicNodeUpdaterOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		count     int64
		err       error
		wantError bool
	}{
		{"success", 200, nil, false}, {"successful zero", 0, nil, false},
		{"fetch failure", 200, errors.New("CNI unavailable"), true}, {"negative", -1, nil, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
			count, fetchErr := int64(250), error(nil)
			u := newTestUpdater(t, updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{AllocatableDirectIPs: count}, fetchErr }), client)
			u.Refresh(nil, nil, nil, nil, nil)
			count, fetchErr = tc.count, tc.err
			u.Refresh(nil, nil, nil, nil, nil)
			want := tc.count
			if tc.wantError {
				want = 0
			}
			node, err := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			quantity := node.Status.Capacity[consts.ResourcePodIPs]
			if quantity.Value() != want {
				t.Fatalf("capacity=%d, want=%d", quantity.Value(), want)
			}
			if s := u.GetSnapshot(); s.Info.AllocatableDirectIPs != want || (s.Err != nil) != tc.wantError {
				t.Fatalf("bad snapshot: %+v", s)
			}
			if ready := general.GetRegisterReadinessCheckResult()[general.HealthzCheckName(u.healthCheckName)].Ready; ready == tc.wantError {
				t.Fatal("bad combined readiness")
			}
			count, fetchErr = 300, nil
			u.Refresh(nil, nil, nil, nil, nil)
			if !general.GetRegisterReadinessCheckResult()[general.HealthzCheckName(u.healthCheckName)].Ready {
				t.Fatal("readiness did not recover")
			}
			s := u.GetSnapshot()
			s.Info.AllocatableDirectIPs = 999
			if u.GetSnapshot().Info.AllocatableDirectIPs != 300 {
				t.Fatal("snapshot mutation changed cache")
			}
		})
	}
}

func TestPeriodicNodeUpdaterPatchPreservesNode(t *testing.T) {
	t.Parallel()
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{"keep": "true"}}, Status: v1.NodeStatus{
		Capacity:    v1.ResourceList{v1.ResourceCPU: resource.MustParse("8"), consts.ResourceNetBandwidth: resource.MustParse("1000")},
		Allocatable: v1.ResourceList{v1.ResourceCPU: resource.MustParse("7")},
		Conditions:  []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}},
	}}
	client := fake.NewSimpleClientset(node)
	u := newTestUpdater(t, updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{AllocatableDirectIPs: 200}, nil }), client)
	u.Refresh(nil, nil, nil, nil, nil)
	got, err := client.CoreV1().Nodes().Get(context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := node.DeepCopy()
	want.Status.Capacity[consts.ResourcePodIPs] = resource.MustParse("200")
	if !equality.Semantic.DeepEqual(got, want) {
		t.Fatalf("unrelated Node fields changed: %+v", got)
	}
	patches := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			patches++
			if action.GetSubresource() != "status" {
				t.Fatal("wrong patch subresource")
			}
		}
	}
	if patches != 1 {
		t.Fatalf("patch count=%d", patches)
	}
	client.ClearActions()
	u.Refresh(nil, nil, nil, nil, nil)
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatal("unchanged capacity patched")
		}
	}
}

func TestPeriodicNodeUpdaterAPIFailure(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"get", "patch"} {
		verb := verb
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			apiErr := errors.New("API denied")
			client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
			u := newTestUpdater(t, updaterFetcherFunc(func(context.Context) (CNIInfo, error) { return CNIInfo{AllocatableDirectIPs: 200}, nil }), client)
			client.PrependReactor(verb, "nodes", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, apiErr })
			if err := u.patchNodeStatus(context.Background(), CNIInfo{AllocatableDirectIPs: 200}); !errors.Is(err, apiErr) {
				t.Fatalf("API error lost: %v", err)
			}
			u.Refresh(nil, nil, nil, nil, nil)
			if general.GetRegisterReadinessCheckResult()[general.HealthzCheckName(u.healthCheckName)].Ready {
				t.Fatal("publication failure marked healthy")
			}
			if s := u.GetSnapshot(); s.Info.AllocatableDirectIPs != 200 || s.Err != nil {
				t.Fatal("API failure confused with CNI snapshot")
			}
		})
	}
}

func TestPeriodicNodeUpdaterTimeout(t *testing.T) {
	t.Parallel()
	client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: v1.NodeStatus{Capacity: v1.ResourceList{consts.ResourcePodIPs: resource.MustParse("200")}}})
	u := newTestUpdater(t, updaterFetcherFunc(func(ctx context.Context) (CNIInfo, error) { <-ctx.Done(); return CNIInfo{}, ctx.Err() }), client)
	u.Refresh(nil, nil, nil, nil, nil)
	if s := u.GetSnapshot(); s.Info != (CNIInfo{}) || !errors.Is(s.Err, context.DeadlineExceeded) {
		t.Fatalf("bad timeout: %+v", s)
	}
	node, err := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := node.Status.Capacity[consts.ResourcePodIPs]
	if q.Value() != 0 {
		t.Fatal("timeout retained positive capacity")
	}
}

func TestPeriodicNodeUpdaterNonblockingSnapshot(t *testing.T) {
	t.Parallel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	u := newTestUpdater(t, updaterFetcherFunc(func(ctx context.Context) (CNIInfo, error) {
		close(entered)
		select {
		case <-release:
			return CNIInfo{AllocatableDirectIPs: 200}, nil
		case <-ctx.Done():
			return CNIInfo{}, ctx.Err()
		}
	}), client)
	defer func() { close(release); <-done }()
	go func() { u.Refresh(nil, nil, nil, nil, nil); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fetch did not start")
	}
	read := make(chan Snapshot, 1)
	go func() { read <- u.GetSnapshot() }()
	select {
	case s := <-read:
		if s.Err == nil {
			t.Fatal("initial error missing")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot blocked on fetch")
	}
}
