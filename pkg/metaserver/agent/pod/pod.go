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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	metricsNamePodCacheSync       = "pod_cache_sync"
	metricsNamePodCacheTotalCount = "pod_cache_total_count"
	metricsNamePodCacheNotFound   = "pod_cache_not_found"
	metricsNamePodFetcherHealth   = "pod_fetcher_health"
)

type ContextKey string

type ContainerInfo struct {
	SandboxID   string `json:"sandboxID"`
	RuntimeType string `json:"runtimeType"`
}

const (
	BypassCacheKey       ContextKey = "bypass_cache"
	StrictBypassCacheKey ContextKey = "strict_bypass_cache"
	BypassCacheTrue      ContextKey = "true"

	podFetcherKubeletHealthCheckName = "pod_fetcher_kubelet"
	podFetcherRuntimeHealthCheckName = "pod_fetcher_runtime"
	tolerationTurns                  = 3
)

type PodFetcher interface {
	KubeletPodFetcher

	// Run starts the preparing logic to collect pod metadata.
	Run(ctx context.Context)

	// GetContainerID & GetContainerSpec are used to parse running container info
	GetContainerID(podUID, containerName string) (string, error)
	GetContainerSpec(podUID, containerName string) (*v1.Container, error)
	// GetPod returns Pod by UID
	GetPod(ctx context.Context, podUID string) (*v1.Pod, error)
}

// CachedPodSnapshotFetcher returns a deep-copied, cache-only Pod snapshot. It
// never synchronizes kubelet; a nil or empty cache is therefore a valid
// observation, not proof that Pods are absent. Callers requiring freshness
// must obtain one explicit strict snapshot after cache-only resolution.
type CachedPodSnapshotFetcher interface {
	GetPodListFromCache(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error)
}

type ContainerIdentityFetcher interface {
	GetContainerIDWithContext(ctx context.Context, podUID, containerName string) (string, error)
	RefreshKubeletPodCache(ctx context.Context) error
	IsContainerRunningInRuntime(
		ctx context.Context,
		podUID, containerName, containerID string,
	) (bool, error)
}

type KubeletPodCacheSyncEvent struct {
	CgroupCreated bool
	Revision      uint64
	SyncedAt      time.Time
}

type KubeletPodCacheSyncEventRegistrar interface {
	RegisterKubeletPodCacheSyncListener(name string) (
		events <-chan KubeletPodCacheSyncEvent,
		unregister func(),
	)
}

type podFetcherImpl struct {
	kubeletPodFetcher    KubeletPodFetcher
	runtimePodFetcher    RuntimePodFetcher
	kataContainerFetcher *KataContainerFetcher

	kubeletPodsCache               map[string]*v1.Pod
	kubeletPodsContinuesEmptyCount int
	kubeletPodsCacheSkipEmptyError bool
	kubeletPodsCacheLock           sync.RWMutex
	kubeletPodSyncRequestSequence  uint64
	kubeletPodSyncCommitSequence   uint64

	runtimePodsCache     map[string]*RuntimePod
	runtimePodsCacheLock sync.RWMutex

	emitter metrics.MetricEmitter

	baseConf        *global.BaseConfiguration
	podConf         *metaserver.PodConfiguration
	cgroupRootPaths []string

	kubeletPodCacheSyncListenersLock sync.Mutex
	kubeletPodCacheSyncListeners     map[string]chan KubeletPodCacheSyncEvent
	kubeletPodCacheRevision          uint64
	cgroupCreateResyncDelays         []time.Duration
	// cgroupCreateResyncStepCh, when non-nil, gates each bounded-resync step:
	// the loop waits for a token on this channel before performing a step.
	// Tests use it to drive the loop step-by-step instead of relying on
	// wall-clock delays; production leaves it nil.
	cgroupCreateResyncStepCh chan struct{}
}

var (
	_ ContainerIdentityFetcher = (*podFetcherImpl)(nil)
	_ CachedPodSnapshotFetcher = (*podFetcherImpl)(nil)
)

func NewPodFetcher(
	baseConf *global.BaseConfiguration, podConf *metaserver.PodConfiguration,
	emitter metrics.MetricEmitter, cgroupRootPaths []string,
) (PodFetcher, error) {
	runtimePodFetcher, err := NewRuntimePodFetcher(baseConf)
	if err != nil {
		klog.Errorf("init runtime pod fetcher failed: %v", err)
		runtimePodFetcher = nil
	}

	initializeRuntimeCgroupPathHandlers(runtimePodFetcher)

	initializeRuntimeCgroupPathHandlers(runtimePodFetcher)

	return &podFetcherImpl{
		kubeletPodFetcher:            NewKubeletPodFetcher(baseConf),
		runtimePodFetcher:            runtimePodFetcher,
		emitter:                      emitter,
		baseConf:                     baseConf,
		podConf:                      podConf,
		cgroupRootPaths:              cgroupRootPaths,
		kubeletPodCacheSyncListeners: make(map[string]chan KubeletPodCacheSyncEvent),
		cgroupCreateResyncDelays: []time.Duration{
			0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second,
		},
	}, nil
}

func (w *podFetcherImpl) RegisterKubeletPodCacheSyncListener(name string) (
	<-chan KubeletPodCacheSyncEvent, func(),
) {
	w.kubeletPodCacheSyncListenersLock.Lock()
	defer w.kubeletPodCacheSyncListenersLock.Unlock()
	if w.kubeletPodCacheSyncListeners == nil {
		w.kubeletPodCacheSyncListeners = make(map[string]chan KubeletPodCacheSyncEvent)
	}
	if old := w.kubeletPodCacheSyncListeners[name]; old != nil {
		close(old)
	}
	events := make(chan KubeletPodCacheSyncEvent, 1)
	w.kubeletPodCacheSyncListeners[name] = events
	return events, func() {
		w.kubeletPodCacheSyncListenersLock.Lock()
		defer w.kubeletPodCacheSyncListenersLock.Unlock()
		if current := w.kubeletPodCacheSyncListeners[name]; current == events {
			delete(w.kubeletPodCacheSyncListeners, name)
			close(events)
		}
	}
}

func (w *podFetcherImpl) publishKubeletPodCacheSyncEvent(event KubeletPodCacheSyncEvent) {
	w.kubeletPodCacheSyncListenersLock.Lock()
	defer w.kubeletPodCacheSyncListenersLock.Unlock()
	for _, listener := range w.kubeletPodCacheSyncListeners {
		select {
		case listener <- event:
			continue
		default:
		}
		select {
		case <-listener:
		default:
		}
		select {
		case listener <- event:
		default:
		}
	}
}

func (w *podFetcherImpl) GetContainerSpec(podUID, containerName string) (*v1.Container, error) {
	if w == nil {
		return nil, fmt.Errorf("get container spec from nil pod fetcher")
	}

	kubeletPodsCache, err := w.getKubeletPodsCache(context.Background())
	if err != nil {
		return nil, fmt.Errorf("getKubeletPodsCache failed with error: %v", err)
	}

	if kubeletPodsCache[podUID] == nil {
		return nil, fmt.Errorf("pod of uid: %s isn't found", podUID)
	}

	for i := range kubeletPodsCache[podUID].Spec.Containers {
		if kubeletPodsCache[podUID].Spec.Containers[i].Name == containerName {
			return kubeletPodsCache[podUID].Spec.Containers[i].DeepCopy(), nil
		}
	}

	return nil, fmt.Errorf("container: %s isn't found in pod: %s spec", containerName, podUID)
}

func (w *podFetcherImpl) GetContainerID(podUID, containerName string) (string, error) {
	return w.GetContainerIDWithContext(context.Background(), podUID, containerName)
}

// RefreshKubeletPodCache synchronously refreshes the pod identity source and
// reports refresh failures instead of falling back to stale cached data.
func (w *podFetcherImpl) RefreshKubeletPodCache(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("refresh kubelet pod cache from nil pod fetcher")
	}
	return w.syncKubeletPodWithContext(ctx)
}

func (w *podFetcherImpl) IsContainerRunningInRuntime(
	ctx context.Context,
	podUID, containerName, containerID string,
) (bool, error) {
	if w == nil || w.runtimePodFetcher == nil {
		return false, fmt.Errorf("runtime pod fetcher is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pods, err := w.runtimePodFetcher.GetPods(false)
	if err != nil {
		return false, fmt.Errorf("list running runtime containers: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, pod := range pods {
		if pod == nil || string(pod.UID) != podUID {
			continue
		}
		for _, container := range pod.Containers {
			if container != nil && container.Id == containerID &&
				container.Metadata != nil && container.Metadata.Name == containerName {
				return true, nil
			}
		}
	}
	return false, nil
}

func (w *podFetcherImpl) GetContainerIDWithContext(ctx context.Context, podUID, containerName string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("get container id from nil pod fetcher")
	}

	kubeletPodsCache, err := w.getKubeletPodsCache(ctx)
	if err != nil {
		return "", fmt.Errorf("getKubeletPodsCache failed: %w", err)
	}

	pod := kubeletPodsCache[podUID]
	if pod == nil {
		return "", fmt.Errorf("%w: uid=%s", ErrPodNotFound, podUID)
	}

	containerID, err := native.GetContainerID(pod, containerName)
	if err != nil {
		return "", fmt.Errorf("%w: pod=%s container=%s: %v",
			ErrContainerNotFound, podUID, containerName, err)
	}
	return containerID, nil
}

// startSyncKubeletPods starts the kubelet pod cache synchronization loop driven by
// cgroup directory changes and a periodic fallback timer.
//
// Directory watching (root cgroup paths plus their first-level pod-level child
// directories) is delegated to general.RegisterSubDirEventWatcher, which reports
// a "needs-sync" signal whenever a watched root or child directory is created or
// removed. On top of that signal this function applies:
//   - a rate limiter to bound sync frequency for filesystem-triggered events;
//   - a periodic timer to ensure syncs happen even without filesystem notifications.
//
// The loop exits when ctx is canceled, at which point the underlying watcher
// is closed by the util.
func (w *podFetcherImpl) startSyncKubeletPods(ctx context.Context) {
	syncCh, watchList, err := general.RegisterSubDirEventWatcher(ctx.Done(), general.SubDirWatcherInfo{
		RootPaths: w.cgroupRootPaths,
	})
	if err != nil {
		klog.Fatalf("init kubelet pod cgroup watcher failed: %v", err)
	}

	timer := time.NewTimer(w.podConf.KubeletPodCacheSyncPeriod)
	rateLimiter := rate.NewLimiter(w.podConf.KubeletPodCacheSyncMaxRate, w.podConf.KubeletPodCacheSyncBurstBulk)

	go func() {
		defer timer.Stop()
		for {
			select {
			case event, ok := <-syncCh:
				if !ok {
					return
				}
				if event.Created {
					if err := w.runCgroupCreateBoundedResync(ctx, rateLimiter); err != nil {
						return
					}
				} else {
					if err := rateLimiter.Wait(ctx); err != nil {
						return
					}
					_ = w.syncKubeletPodWithContext(ctx)
				}
				resetTimer(timer, w.podConf.KubeletPodCacheSyncPeriod)
			case <-timer.C:
				klog.Infof("cgroup watch list %v", watchList())
				w.syncKubeletPod(ctx)
				resetTimer(timer, w.podConf.KubeletPodCacheSyncPeriod)
			case <-ctx.Done():
				klog.Infof("file event watcher stopped")
				klog.Infof("stop timer channel when ctx.Done() has been received")
				return
			}
		}
	}()
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (w *podFetcherImpl) runCgroupCreateBoundedResync(
	ctx context.Context, rateLimiter *rate.Limiter,
) error {
	delays := w.cgroupCreateResyncDelays
	if len(delays) == 0 {
		delays = []time.Duration{0}
	}
	for _, delay := range delays {
		// In tests, gate each step on an explicit token so the consumer can
		// drain between publishes; production leaves the channel nil.
		if w.cgroupCreateResyncStepCh != nil {
			select {
			case <-w.cgroupCreateResyncStepCh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			}
		}
		if err := rateLimiter.Wait(ctx); err != nil {
			return err
		}
		if err := w.syncKubeletPodWithContext(ctx); err != nil {
			continue
		}
		w.publishKubeletPodCacheSyncEvent(KubeletPodCacheSyncEvent{
			CgroupCreated: true,
			Revision:      w.currentKubeletPodCacheRevision(),
			SyncedAt:      time.Now(),
		})
	}
	return nil
}

func (w *podFetcherImpl) Run(ctx context.Context) {
	general.RegisterHeartbeatCheck(podFetcherKubeletHealthCheckName, tolerationTurns*w.podConf.KubeletPodCacheSyncPeriod,
		general.HealthzCheckStateNotReady, tolerationTurns*w.podConf.KubeletPodCacheSyncPeriod)
	general.RegisterHeartbeatCheck(podFetcherRuntimeHealthCheckName, tolerationTurns*w.podConf.RuntimePodCacheSyncPeriod,
		general.HealthzCheckStateNotReady, tolerationTurns*w.podConf.RuntimePodCacheSyncPeriod)

	w.startSyncKubeletPods(ctx)
	go wait.UntilWithContext(ctx, w.syncRuntimePod, w.podConf.RuntimePodCacheSyncPeriod)
	go wait.Until(w.checkPodCache, 30*time.Second, ctx.Done())
	<-ctx.Done()
}

func (w *podFetcherImpl) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	kubeletPodsCache, err := w.getKubeletPodsCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("getKubeletPodsCache failed with error: %w", err)
	}

	w.kubeletPodsCacheLock.RLock()
	defer w.kubeletPodsCacheLock.RUnlock()

	res := make([]*v1.Pod, 0, len(kubeletPodsCache))
	for _, p := range kubeletPodsCache {
		if podFilter != nil && !podFilter(p) {
			continue
		}
		res = append(res, p.DeepCopy())
	}

	return res, nil
}

func (w *podFetcherImpl) GetPodListFromCache(
	ctx context.Context,
	podFilter func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if w == nil {
		return nil, fmt.Errorf("get cached pod snapshot from nil pod fetcher")
	}
	if err := lockRLockContext(ctx, &w.kubeletPodsCacheLock); err != nil {
		return nil, err
	}
	defer w.kubeletPodsCacheLock.RUnlock()

	res := make([]*v1.Pod, 0, len(w.kubeletPodsCache))
	for _, pod := range w.kubeletPodsCache {
		if pod == nil || podFilter != nil && !podFilter(pod) {
			continue
		}
		res = append(res, pod.DeepCopy())
	}
	return res, nil
}

func (w *podFetcherImpl) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	kubeletPodsCache, err := w.getKubeletPodsCache(ctx)
	if err != nil {
		if ctx.Value(StrictBypassCacheKey) == BypassCacheTrue {
			return nil, err
		}
		return nil, fmt.Errorf("getKubeletPodsCache failed with error: %v", err)
	}
	if pod, ok := kubeletPodsCache[podUID]; ok {
		return pod, nil
	}
	return nil, NewPodNotFoundError(podUID)
}

func (w *podFetcherImpl) getKubeletPodsCache(ctx context.Context) (map[string]*v1.Pod, error) {
	// if current kubelet pod cache is nil or enforce bypass, we sync cache first
	if err := lockRLockContext(ctx, &w.kubeletPodsCacheLock); err != nil {
		return nil, err
	}
	strictBypass := ctx.Value(StrictBypassCacheKey) == BypassCacheTrue
	if w.kubeletPodsCache == nil || len(w.kubeletPodsCache) == 0 ||
		ctx.Value(BypassCacheKey) == BypassCacheTrue || strictBypass {
		w.kubeletPodsCacheLock.RUnlock()
		if err := w.syncKubeletPodWithContext(ctx); err != nil {
			if strictBypass {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	} else {
		w.kubeletPodsCacheLock.RUnlock()
	}

	// if kubelet returns empty pod list continuously for specified times of running syncKubeletPod,
	// which means there is indeed no pod scheduled on node, so we should not return error, otherwise we should return an error,
	// which means something is wrong with running syncKubeletPod
	if err := lockRLockContext(ctx, &w.kubeletPodsCacheLock); err != nil {
		return nil, err
	}
	defer w.kubeletPodsCacheLock.RUnlock()
	if !w.kubeletPodsCacheSkipEmptyError && (w.kubeletPodsCache == nil || len(w.kubeletPodsCache) == 0) {
		return nil, fmt.Errorf("first sync kubelet pod cache failed")
	}

	return w.kubeletPodsCache, nil
}

// syncRuntimePod sync local runtime pod cache from runtime pod fetcher.
func (w *podFetcherImpl) syncRuntimePod(_ context.Context) {
	if w.runtimePodFetcher == nil {
		klog.Error("runtime pod fetcher init not success")
		_ = w.emitter.StoreInt64("pod_cache_runtime_init_failed", 1, metrics.MetricTypeNameRaw)
		return
	}

	runtimePods, err := w.runtimePodFetcher.GetPods(false)
	_ = general.UpdateHealthzStateByError(podFetcherRuntimeHealthCheckName, err)
	if err != nil {
		klog.Errorf("sync runtime pod failed: %s", err)
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "runtime",
				"success": "false",
			})...)
		return
	}

	_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			"source":  "runtime",
			"success": "true",
		})...)

	runtimePodsCache := make(map[string]*RuntimePod, len(runtimePods))

	for _, p := range runtimePods {
		runtimePodsCache[string(p.UID)] = p
	}

	w.runtimePodsCacheLock.Lock()
	w.runtimePodsCache = runtimePodsCache
	w.runtimePodsCacheLock.Unlock()
}

// syncKubeletPod sync local kubelet pod cache from kubelet pod fetcher.
func (w *podFetcherImpl) syncKubeletPod(ctx context.Context) {
	_ = w.syncKubeletPodWithContext(ctx)
}

func (w *podFetcherImpl) syncKubeletPodWithContext(ctx context.Context) error {
	requestSequence := atomic.AddUint64(&w.kubeletPodSyncRequestSequence, 1)
	klog.Infof("sync kubelet pod")
	kubeletPods, err := w.kubeletPodFetcher.GetPodList(ctx, nil)
	_ = general.UpdateHealthzStateByError(podFetcherKubeletHealthCheckName, err)
	if err != nil {
		klog.Errorf("sync kubelet pod failed: %s", err)
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "error",
			})...)
		return err
	} else if len(kubeletPods) == 0 {
		klog.Error("kubelet pod is empty")
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "empty",
			})...)
	} else {
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "true",
			})...)
	}

	if klog.V(5).Enabled() {
		klog.Infof("sync kubelet pod success")
		for _, pod := range kubeletPods {
			klog.InfoS("dump pod", "pod", pod.String())
		}
	}

	kubeletPodsCache := make(map[string]*v1.Pod, len(kubeletPods))

	for _, p := range kubeletPods {
		kubeletPodsCache[string(p.GetUID())] = p
	}

	if err := lockContext(ctx, &w.kubeletPodsCacheLock); err != nil {
		return err
	}
	if requestSequence < w.kubeletPodSyncCommitSequence {
		w.kubeletPodsCacheLock.Unlock()
		return nil
	}
	w.kubeletPodsCache = kubeletPodsCache
	w.kubeletPodSyncCommitSequence = requestSequence
	w.kubeletPodCacheRevision++
	if len(kubeletPodsCache) == 0 {
		w.kubeletPodsContinuesEmptyCount++
	} else {
		w.kubeletPodsContinuesEmptyCount = 0
	}
	w.kubeletPodsCacheSkipEmptyError = w.kubeletPodsContinuesEmptyCount >= w.podConf.KubeletPodCacheSyncEmptyThreshold
	w.kubeletPodsCacheLock.Unlock()
	return nil
}

func (w *podFetcherImpl) currentKubeletPodCacheRevision() uint64 {
	w.kubeletPodsCacheLock.RLock()
	defer w.kubeletPodsCacheLock.RUnlock()
	return w.kubeletPodCacheRevision
}

func lockRLockContext(ctx context.Context, lock *sync.RWMutex) error {
	return waitForCacheLock(ctx, lock.TryRLock)
}

func lockContext(ctx context.Context, lock *sync.RWMutex) error {
	return waitForCacheLock(ctx, lock.TryLock)
}

func waitForCacheLock(ctx context.Context, tryLock func() bool) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if tryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// checkPodCache if the runtime pod and kubelet pod match, and send a metric alert if they don't.
func (w *podFetcherImpl) checkPodCache() {
	w.kubeletPodsCacheLock.RLock()
	kubeletPodsCache := w.kubeletPodsCache
	w.kubeletPodsCacheLock.RUnlock()

	w.runtimePodsCacheLock.RLock()
	runtimePodsCache := w.runtimePodsCache
	w.runtimePodsCacheLock.RUnlock()

	_ = w.emitter.StoreInt64(metricsNamePodFetcherHealth, 1, metrics.MetricTypeNameRaw)

	klog.Infof("total kubelet pod count is %d", len(kubeletPodsCache))
	_ = w.emitter.StoreInt64(metricsNamePodCacheTotalCount, int64(len(kubeletPodsCache)), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			"source": "kubelet",
		})...)

	klog.Infof("total runtime pod count is %d", len(runtimePodsCache))
	_ = w.emitter.StoreInt64(metricsNamePodCacheTotalCount, int64(len(runtimePodsCache)), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			"source": "runtime",
		})...)

	runtimeNotFoundPodCount := 0
	for id, p := range kubeletPodsCache {
		// we only care about running kubelet pods here, because pods in other stages may not exist in runtime
		if _, ok := runtimePodsCache[id]; !ok && p.Status.Phase == v1.PodRunning {
			klog.Warningf("running kubelet pod %s/%s with uid %s runtime not found", p.Namespace, p.Name, p.UID)
			runtimeNotFoundPodCount += 1
		}
	}
	_ = w.emitter.StoreInt64(metricsNamePodCacheNotFound, int64(runtimeNotFoundPodCount), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			"source": "runtime",
		})...)

	kubeletNotFoundPodCount := 0
	for id, p := range runtimePodsCache {
		if _, ok := kubeletPodsCache[id]; !ok {
			klog.Warningf("runtime pod %s/%s with uid %s kubelet not found", p.Namespace, p.Name, p.UID)
			kubeletNotFoundPodCount += 1
		}
	}
	_ = w.emitter.StoreInt64(metricsNamePodCacheNotFound, int64(kubeletNotFoundPodCount), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			"source": "kubelet",
		})...)
}
