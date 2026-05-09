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
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"

	metaserverconf "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func Test_getCgroupRootPaths(t *testing.T) {
	t.Parallel()

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
			KubeletPodCacheSyncMaxRate:   rate.Limit(100),
			KubeletPodCacheSyncBurstBulk: 100,
		},
		cgroupRootPaths: []string{rootDir},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pf.startSyncKubeletPods(ctx)

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

	afterRemoveCount := atomic.LoadInt32(&fetcher.callCount)
	newPodDir := filepath.Join(rootDir, "pod-new")
	if err := os.Mkdir(newPodDir, 0o755); err != nil {
		t.Fatalf("create new pod dir failed: %v", err)
	}
	waitForCondition(t, func() bool {
		return atomic.LoadInt32(&fetcher.callCount) >= afterRemoveCount+1
	}, "new pod dir creation should trigger sync")

	afterCreateCount := atomic.LoadInt32(&fetcher.callCount)
	containerDir := filepath.Join(newPodDir, "container-new")
	if err := os.Mkdir(containerDir, 0o755); err != nil {
		t.Fatalf("create container dir failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&fetcher.callCount); got != afterCreateCount {
		t.Fatalf("container dir creation should not trigger sync, got call count %d want %d", got, afterCreateCount)
	}
}
