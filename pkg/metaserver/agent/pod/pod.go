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
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
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

const (
	BypassCacheKey  ContextKey = "bypass_cache"
	BypassCacheTrue ContextKey = "true"
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

type podFetcherImpl struct {
	kubeletPodFetcher KubeletPodFetcher
	runtimePodFetcher RuntimePodFetcher

	kubeletPodsCache     map[string]*v1.Pod
	kubeletPodsCacheLock sync.RWMutex
	runtimePodsCache     map[string]*RuntimePod
	runtimePodsCacheLock sync.RWMutex

	emitter metrics.MetricEmitter

	baseConf        *global.BaseConfiguration
	podConf         *metaserver.PodConfiguration
	cgroupRootPaths []string
}

func NewPodFetcher(baseConf *global.BaseConfiguration, podConf *metaserver.PodConfiguration,
	emitter metrics.MetricEmitter) (PodFetcher, error) {
	runtimePodFetcher, err := NewRuntimePodFetcher(baseConf)
	if err != nil {
		klog.Errorf("init runtime pod fetcher failed: %v", err)
		runtimePodFetcher = nil
	}

	return &podFetcherImpl{
		kubeletPodFetcher: NewKubeletPodFetcher(baseConf),
		runtimePodFetcher: runtimePodFetcher,
		emitter:           emitter,
		baseConf:          baseConf,
		podConf:           podConf,
		cgroupRootPaths:   common.GetKubernetesCgroupRootPathWithSubSys(common.DefaultSelectedSubsys),
	}, nil
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
	if w == nil {
		return "", fmt.Errorf("get container id from nil pod fetcher")
	}

	kubeletPodsCache, err := w.getKubeletPodsCache(context.Background())
	if err != nil {
		return "", fmt.Errorf("getKubeletPodsCache failed with error: %v", err)
	}

	pod := kubeletPodsCache[podUID]
	if pod == nil {
		return "", fmt.Errorf("pod of uid: %s isn't found", podUID)
	}

	return native.GetContainerID(pod, containerName)
}

func (w *podFetcherImpl) Run(ctx context.Context) {
	watcherInfo := general.FileWatcherInfo{
		Path:     w.cgroupRootPaths,
		Filename: "",
		Op:       fsnotify.Create,
	}

	watcherCh, err := general.RegisterFileEventWatcher(ctx.Done(), watcherInfo)
	if err != nil {
		klog.Fatalf("register file event watcher failed: %s", err)
	}

	timer := time.NewTimer(w.podConf.KubeletPodCacheSyncPeriod)
	rateLimiter := rate.NewLimiter(w.podConf.KubeletPodCacheSyncMaxRate, w.podConf.KubeletPodCacheSyncBurstBulk)

	go func() {
		for {
			select {
			case <-watcherCh:
				if rateLimiter.Allow() {
					w.syncKubeletPod(ctx)
					timer.Reset(w.podConf.KubeletPodCacheSyncPeriod)
				}
			case <-timer.C:
				w.syncKubeletPod(ctx)
				timer.Reset(w.podConf.KubeletPodCacheSyncPeriod)
			case <-ctx.Done():
				klog.Infof("file event watcher stopped")
				klog.Infof("stop timer channel when ctx.Done() has been received")
				timer.Stop()
				return
			}
		}
	}()

	go wait.UntilWithContext(ctx, w.syncRuntimePod, w.podConf.RuntimePodCacheSyncPeriod)
	go wait.Until(w.checkPodCache, 30*time.Second, ctx.Done())
	<-ctx.Done()
}

func (w *podFetcherImpl) GetPodList(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
	kubeletPodsCache, err := w.getKubeletPodsCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("getKubeletPodsCache failed with error: %v", err)
	}

	res := make([]*v1.Pod, 0, len(kubeletPodsCache))
	for _, p := range kubeletPodsCache {
		if podFilter != nil && !podFilter(p) {
			continue
		}
		res = append(res, p.DeepCopy())
	}

	return res, nil
}

func (w *podFetcherImpl) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	kubeletPodsCache, err := w.getKubeletPodsCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("getKubeletPodsCache failed with error: %v", err)
	}
	if pod, ok := kubeletPodsCache[podUID]; ok {
		return pod, nil
	}
	return nil, fmt.Errorf("failed to find pod by uid %v", podUID)
}

func (w *podFetcherImpl) getKubeletPodsCache(ctx context.Context) (map[string]*v1.Pod, error) {
	// if current kubelet pod cache is nil or enforce bypass, we sync cache first
	w.kubeletPodsCacheLock.RLock()
	if w.kubeletPodsCache == nil || len(w.kubeletPodsCache) == 0 || ctx.Value(BypassCacheKey) == BypassCacheTrue {
		w.kubeletPodsCacheLock.RUnlock()
		w.syncKubeletPod(ctx)
	} else {
		w.kubeletPodsCacheLock.RUnlock()
	}

	// the second time checks if the kubelet pod cache is nil, if it is,
	// it means the first sync of the kubelet pod failed and returns an error
	w.kubeletPodsCacheLock.RLock()
	defer w.kubeletPodsCacheLock.RUnlock()
	if w.kubeletPodsCache == nil || len(w.kubeletPodsCache) == 0 {
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
	kubeletPods, err := w.kubeletPodFetcher.GetPodList(ctx, nil)
	if err != nil {
		klog.Errorf("sync kubelet pod failed: %s", err)
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "error",
			})...)
		return
	} else if len(kubeletPods) == 0 {
		klog.Error("kubelet pod is empty")
		_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"source":  "kubelet",
				"success": "false",
				"reason":  "empty",
			})...)
		return
	}

	_ = w.emitter.StoreInt64(metricsNamePodCacheSync, 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			"source":  "kubelet",
			"success": "true",
		})...)

	kubeletPodsCache := make(map[string]*v1.Pod, len(kubeletPods))

	for _, p := range kubeletPods {
		kubeletPodsCache[string(p.GetUID())] = p
	}

	w.kubeletPodsCacheLock.Lock()
	w.kubeletPodsCache = kubeletPodsCache
	w.kubeletPodsCacheLock.Unlock()
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
