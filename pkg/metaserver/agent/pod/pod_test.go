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

package pod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	metaserverconf "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func Test_getCgroupRootPaths(t *testing.T) {
	t.Parallel()

	const subprocessEnv = "KATALYST_TEST_GET_CGROUP_ROOT_PATHS_SUBPROCESS"
	if os.Getenv(subprocessEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^Test_getCgroupRootPaths$")
		cmd.Env = append(os.Environ(), subprocessEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("subprocess failed: %v\n%s", err, output)
		}
		return
	}

	// The expected paths use the legacy cgroup v1 layout (a per-controller
	// /cpu/ prefix). Pin cgroup v2 detection to v1 in this subprocess so the
	// path helpers are exercised deterministically regardless of the host's
	// cgroup version; the mount root itself stays the standard /sys/fs/cgroup.
	undo := common.SetCgroup2UnifiedModeOverride(false)
	defer undo()

	want := []string{
		"/sys/fs/cgroup/cpu/kubepods",
		"/sys/fs/cgroup/cpu/kubepods/besteffort",
		"/sys/fs/cgroup/cpu/kubepods/burstable",
	}

	if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
		t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
	}

	common.InitKubernetesCGroupPath(common.CgroupTypeSystemd, []string{"/kubepods/test.slice"})

	want = []string{
		"/sys/fs/cgroup/cpu/kubepods.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-besteffort.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-burstable.slice",
		"/sys/fs/cgroup/cpu/kubepods/test.slice",
	}

	if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
		t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
	}
}

type countingKubeletPodFetcher struct {
	callCount int32
}

func (f *countingKubeletPodFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	atomic.AddInt32(&f.callCount, 1)
	return []*v1.Pod{{}}, nil
}

type delayedVisibilityKubeletPodFetcher struct {
	callCount int32
}

func (f *delayedVisibilityKubeletPodFetcher) GetPodList(
	_ context.Context, _ func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	call := atomic.AddInt32(&f.callCount, 1)
	version := "stale"
	if call >= 3 {
		version = "fresh"
	}
	return []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{
			UID:         "pod",
			Annotations: map[string]string{"version": version},
		},
	}}, nil
}

func TestKubeletPodCacheSyncListenerRegistration(t *testing.T) {
	t.Parallel()

	pf := &podFetcherImpl{}
	first, unregisterFirst := pf.RegisterKubeletPodCacheSyncListener("first")
	second, unregisterSecond := pf.RegisterKubeletPodCacheSyncListener("second")
	defer unregisterSecond()

	pf.publishKubeletPodCacheSyncEvent(KubeletPodCacheSyncEvent{CgroupCreated: true, Revision: 1})
	pf.publishKubeletPodCacheSyncEvent(KubeletPodCacheSyncEvent{CgroupCreated: true, Revision: 2})

	for name, events := range map[string]<-chan KubeletPodCacheSyncEvent{
		"first": first, "second": second,
	} {
		select {
		case event := <-events:
			if event.Revision != 2 || !event.CgroupCreated {
				t.Fatalf("%s listener event = %+v, want latest create revision 2", name, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s listener did not receive cache sync event", name)
		}
	}

	unregisterFirst()
	pf.publishKubeletPodCacheSyncEvent(KubeletPodCacheSyncEvent{CgroupCreated: true, Revision: 3})
	if _, ok := <-first; ok {
		t.Fatal("unregistered listener channel is still open")
	}
}

func TestCgroupCreateRunsBoundedCacheResync(t *testing.T) {
	t.Parallel()

	fetcher := &delayedVisibilityKubeletPodFetcher{}
	stepCh := make(chan struct{})
	pf := &podFetcherImpl{
		kubeletPodFetcher: fetcher,
		emitter:           metrics.DummyMetrics{},
		podConf: &metaserverconf.PodConfiguration{
			KubeletPodCacheSyncPeriod:    time.Hour,
			KubeletPodCacheSyncMaxRate:   rate.Limit(1000),
			KubeletPodCacheSyncBurstBulk: 10,
		},
		kubeletPodCacheSyncListeners: make(map[string]chan KubeletPodCacheSyncEvent),
		// zero per-step delays: the bounded loop still runs once per configured
		// delay, but without real timers; a per-step channel synchronizes the
		// consumer so every publish is observed.
		cgroupCreateResyncDelays: []time.Duration{0, 0, 0},
		cgroupCreateResyncStepCh: stepCh,
	}
	events, unregister := pf.RegisterKubeletPodCacheSyncListener("bounded-resync")
	defer unregister()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Drive the production bounded-resync path directly (the same call
	// startSyncKubeletPods makes on a cgroup-create event) in a background
	// goroutine. The loop pauses after each publish until we drain the event,
	// making the whole sequence deterministic.
	limiter := rate.NewLimiter(rate.Limit(1000), 10)
	go func() { _ = pf.runCgroupCreateBoundedResync(ctx, limiter) }()

	var revisions []uint64
	for len(revisions) < 3 {
		// unblock one bounded-resync step, then drain its published revision.
		stepCh <- struct{}{}
		select {
		case event := <-events:
			revisions = append(revisions, event.Revision)
		case <-time.After(time.Second):
			t.Fatalf("cache sync revisions = %v, want three bounded resync notifications", revisions)
		}
	}
	// each bounded resync must produce a strictly newer revision, never a
	// duplicate/stale revision replayed by the rate limiter.
	for i := 1; i < len(revisions); i++ {
		if revisions[i] <= revisions[i-1] {
			t.Fatalf("cache sync revisions = %v, want strictly increasing", revisions)
		}
	}
	pod, err := pf.GetPod(context.Background(), "pod")
	if err != nil {
		t.Fatalf("GetPod() error = %v", err)
	}
	if got := pod.Annotations["version"]; got != "fresh" {
		t.Fatalf("cache version = %q, want fresh after bounded resync", got)
	}
}

type contextBlockingKubeletPodFetcher struct{}

func (contextBlockingKubeletPodFetcher) GetPodList(ctx context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type errorKubeletPodFetcher struct {
	err error
}

func (f errorKubeletPodFetcher) GetPodList(
	context.Context, func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	return nil, f.err
}

func TestStrictBypassCacheReturnsSyncErrorWithoutStaleFallback(t *testing.T) {
	t.Parallel()

	syncErr := errors.New("strict kubelet sync failed")
	pf := &podFetcherImpl{
		kubeletPodFetcher: errorKubeletPodFetcher{err: syncErr},
		kubeletPodsCache: map[string]*v1.Pod{
			"cached": {ObjectMeta: metav1.ObjectMeta{UID: "cached"}},
		},
		emitter: metrics.DummyMetrics{},
	}
	ctx := context.WithValue(context.Background(), BypassCacheKey, BypassCacheTrue)
	ctx = context.WithValue(ctx, StrictBypassCacheKey, BypassCacheTrue)

	_, err := pf.GetPod(ctx, "cached")
	if err != syncErr {
		t.Fatalf("GetPod() error = %v, want original strict sync error %v", err, syncErr)
	}

	_, err = pf.GetPodList(ctx, nil)
	if !errors.Is(err, syncErr) {
		t.Fatalf("GetPodList() error = %v, want errors.Is(_, %v)", err, syncErr)
	}
}

func TestGetPodListFromCacheNeverSynchronizesKubelet(t *testing.T) {
	t.Parallel()

	for name, cache := range map[string]map[string]*v1.Pod{
		"nil cache": nil,
		"empty map": {},
	} {
		name, cache := name, cache
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			kubelet := &countingKubeletPodFetcher{}
			pf := &podFetcherImpl{
				kubeletPodFetcher: kubelet,
				kubeletPodsCache:  cache,
			}

			pods, err := pf.GetPodListFromCache(context.Background(), nil)
			if err != nil {
				t.Fatalf("GetPodListFromCache() error = %v", err)
			}
			if len(pods) != 0 {
				t.Fatalf("GetPodListFromCache() returned %d pods, want empty", len(pods))
			}
			if got := atomic.LoadInt32(&kubelet.callCount); got != 0 {
				t.Fatalf("kubelet calls = %d, want 0", got)
			}
		})
	}

	t.Run("populated cache returns a deep copy", func(t *testing.T) {
		t.Parallel()

		kubelet := &countingKubeletPodFetcher{}
		cached := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:         "cached",
				Annotations: map[string]string{"version": "cached"},
			},
		}
		pf := &podFetcherImpl{
			kubeletPodFetcher: kubelet,
			kubeletPodsCache:  map[string]*v1.Pod{"cached": cached},
		}

		pods, err := pf.GetPodListFromCache(context.Background(), func(pod *v1.Pod) bool {
			return pod.UID == "cached"
		})
		if err != nil {
			t.Fatalf("GetPodListFromCache() error = %v", err)
		}
		if got := atomic.LoadInt32(&kubelet.callCount); got != 0 {
			t.Fatalf("kubelet sync calls = %d, want zero", got)
		}
		if len(pods) != 1 || pods[0].Annotations["version"] != "cached" {
			t.Fatalf("cached snapshot = %#v, want cached pod", pods)
		}
		pods[0].Annotations["version"] = "mutated"
		if got := cached.Annotations["version"]; got != "cached" {
			t.Fatalf("returned pod was not deep-copied: cache version = %q", got)
		}
	})
}

func TestCacheOnlyThenStrictGetPodListSynchronizesKubeletOnce(t *testing.T) {
	t.Parallel()

	kubelet := &countingKubeletPodFetcher{}
	pf := &podFetcherImpl{
		kubeletPodFetcher: kubelet,
		kubeletPodsCache:  map[string]*v1.Pod{},
		emitter:           metrics.DummyMetrics{},
		podConf:           &metaserverconf.PodConfiguration{},
	}

	cachedPods, err := pf.GetPodListFromCache(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetPodListFromCache() error = %v", err)
	}
	if len(cachedPods) != 0 {
		t.Fatalf("GetPodListFromCache() returned %d pods, want empty", len(cachedPods))
	}
	if got := atomic.LoadInt32(&kubelet.callCount); got != 0 {
		t.Fatalf("kubelet calls after cache-only read = %d, want 0", got)
	}

	strictCtx := context.WithValue(context.Background(), StrictBypassCacheKey, BypassCacheTrue)
	pods, err := pf.GetPodList(strictCtx, nil)
	if err != nil {
		t.Fatalf("strict GetPodList() error = %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("strict GetPodList() returned %d pods, want 1", len(pods))
	}
	if got := atomic.LoadInt32(&kubelet.callCount); got != 1 {
		t.Fatalf("total kubelet calls = %d, want exactly 1", got)
	}
}

type gatedKubeletPodFetcher struct {
	entered chan struct{}
	release chan struct{}
}

func (f gatedKubeletPodFetcher) GetPodList(context.Context, func(*v1.Pod) bool) ([]*v1.Pod, error) {
	close(f.entered)
	<-f.release
	return []*v1.Pod{{}}, nil
}

type orderedKubeletPodFetcher struct {
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	calls         int32
}

func (f *orderedKubeletPodFetcher) GetPodList(context.Context, func(*v1.Pod) bool) ([]*v1.Pod, error) {
	switch atomic.AddInt32(&f.calls, 1) {
	case 1:
		close(f.firstStarted)
		<-f.releaseFirst
		return []*v1.Pod{{ObjectMeta: metav1.ObjectMeta{UID: "old"}}}, nil
	case 2:
		close(f.secondStarted)
		<-f.releaseSecond
		return []*v1.Pod{{ObjectMeta: metav1.ObjectMeta{UID: "new"}}}, nil
	default:
		return nil, fmt.Errorf("unexpected kubelet pod fetch")
	}
}

func TestRefreshKubeletPodCacheDoesNotLetOlderRequestOverwriteNewerResult(t *testing.T) {
	t.Parallel()

	fetcher := &orderedKubeletPodFetcher{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
	pf := &podFetcherImpl{
		kubeletPodFetcher: fetcher,
		emitter:           metrics.DummyMetrics{},
		podConf:           &metaserverconf.PodConfiguration{},
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- pf.RefreshKubeletPodCache(context.Background()) }()
	<-fetcher.firstStarted
	go func() { secondDone <- pf.RefreshKubeletPodCache(context.Background()) }()
	<-fetcher.secondStarted

	close(fetcher.releaseSecond)
	if err := <-secondDone; err != nil {
		t.Fatalf("newer refresh failed: %v", err)
	}
	close(fetcher.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("older refresh failed: %v", err)
	}

	pf.kubeletPodsCacheLock.RLock()
	defer pf.kubeletPodsCacheLock.RUnlock()
	if pf.kubeletPodsCache["new"] == nil || pf.kubeletPodsCache["old"] != nil {
		t.Fatalf("pod cache = %#v, want only newer result", pf.kubeletPodsCache)
	}
}

func TestRefreshKubeletPodCachePropagatesRefreshError(t *testing.T) {
	t.Parallel()

	pf := &podFetcherImpl{
		kubeletPodFetcher: contextBlockingKubeletPodFetcher{},
		emitter:           metrics.DummyMetrics{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := pf.RefreshKubeletPodCache(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RefreshKubeletPodCache() error = %v, want context canceled", err)
	}
}

func TestGetContainerIDWithContextInterruptsCacheSync(t *testing.T) {
	t.Parallel()

	pf := &podFetcherImpl{
		kubeletPodFetcher: contextBlockingKubeletPodFetcher{},
		emitter:           metrics.DummyMetrics{},
		podConf:           &metaserverconf.PodConfiguration{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := pf.GetContainerIDWithContext(ctx, "pod", "container")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetContainerIDWithContext() error = %v, want context deadline exceeded", err)
	}
}

func TestGetKubeletPodsCacheInterruptsLockWait(t *testing.T) {
	t.Parallel()

	pf := &podFetcherImpl{
		kubeletPodsCache: map[string]*v1.Pod{"pod": {}},
	}
	pf.kubeletPodsCacheLock.Lock()
	defer pf.kubeletPodsCacheLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := pf.getKubeletPodsCache(ctx)
		result <- err
	}()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("getKubeletPodsCache() error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("getKubeletPodsCache() did not stop waiting for the cache lock")
	}
}

func TestGetContainerIDWithContextInterruptsCacheWriteLockWait(t *testing.T) {
	t.Parallel()

	fetcher := gatedKubeletPodFetcher{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	pf := &podFetcherImpl{
		kubeletPodFetcher: fetcher,
		emitter:           metrics.DummyMetrics{},
		podConf:           &metaserverconf.PodConfiguration{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := pf.GetContainerIDWithContext(ctx, "pod", "container")
		result <- err
	}()

	<-fetcher.entered
	pf.kubeletPodsCacheLock.Lock()
	defer pf.kubeletPodsCacheLock.Unlock()
	close(fetcher.release)

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("GetContainerIDWithContext() error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("GetContainerIDWithContext() did not stop waiting for the cache write lock")
	}
}

func waitForCondition(t *testing.T, condition func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestStartSyncKubeletPods(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	existingPodDir := filepath.Join(rootDir, "pod-existing")
	if err := os.Mkdir(existingPodDir, 0o755); err != nil {
		t.Fatalf("create existing pod dir failed: %v", err)
	}

	fetcher := &countingKubeletPodFetcher{}
	pf := &podFetcherImpl{
		kubeletPodFetcher: fetcher,
		emitter:           metrics.DummyMetrics{},
		podConf: &metaserverconf.PodConfiguration{
			KubeletPodCacheSyncPeriod:    time.Hour,
			KubeletPodCacheSyncMaxRate:   rate.Limit(5),
			KubeletPodCacheSyncBurstBulk: 1,
		},
		cgroupRootPaths:              []string{rootDir},
		kubeletPodCacheSyncListeners: make(map[string]chan KubeletPodCacheSyncEvent),
		cgroupCreateResyncDelays:     []time.Duration{0},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pf.startSyncKubeletPods(ctx)
	createEvents, unregister := pf.RegisterKubeletPodCacheSyncListener("test")
	defer unregister()

	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case <-createEvents:
		default:
			goto initialized
		}
	}
initialized:
	initialCount := atomic.LoadInt32(&fetcher.callCount)

	if err := os.WriteFile(filepath.Join(rootDir, "root-file"), []byte("test"), 0o644); err != nil {
		t.Fatalf("create root file failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&fetcher.callCount); got != initialCount {
		t.Fatalf("root file creation should not trigger sync, got call count %d want %d", got, initialCount)
	}

	if err := os.Remove(existingPodDir); err != nil {
		t.Fatalf("remove existing pod dir failed: %v", err)
	}
	waitForCondition(t, func() bool {
		return atomic.LoadInt32(&fetcher.callCount) >= initialCount+1
	}, "existing pod dir removal should trigger sync")
	select {
	case <-createEvents:
		t.Fatal("pod cgroup removal should not publish a create event")
	case <-time.After(100 * time.Millisecond):
	}

	afterRemoveCount := atomic.LoadInt32(&fetcher.callCount)
	newPodDir := filepath.Join(rootDir, "pod-new")
	if err := os.Mkdir(newPodDir, 0o755); err != nil {
		t.Fatalf("create new pod dir failed: %v", err)
	}
	waitForCondition(t, func() bool {
		return atomic.LoadInt32(&fetcher.callCount) >= afterRemoveCount+1
	}, "new pod dir creation should trigger sync")
	select {
	case <-createEvents:
	case <-time.After(2 * time.Second):
		t.Fatal("pod cgroup creation should publish an event after cache sync")
	}

	afterCreateCount := atomic.LoadInt32(&fetcher.callCount)
	containerDir := filepath.Join(newPodDir, "container-new")
	if err := os.Mkdir(containerDir, 0o755); err != nil {
		t.Fatalf("create container dir failed: %v", err)
	}
	waitForCondition(t, func() bool {
		return atomic.LoadInt32(&fetcher.callCount) >= afterCreateCount+1
	}, "container dir creation should trigger sync")
	select {
	case <-createEvents:
	case <-time.After(2 * time.Second):
		t.Fatal("container cgroup creation should publish an event after cache sync")
	}
}

func TestResetTimerDropsExpiredTick(t *testing.T) {
	t.Parallel()

	timer := time.NewTimer(time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	resetTimer(timer, 50*time.Millisecond)

	select {
	case <-timer.C:
		t.Fatal("reset timer delivered an expired tick")
	case <-time.After(20 * time.Millisecond):
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
