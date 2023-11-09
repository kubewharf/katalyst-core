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

package dynamicpolicy

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/oom"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func (p *DynamicPolicy) PollOOMBPFInit(stopCh <-chan struct{}) {
	err := wait.PollImmediateUntil(5*time.Second, func() (bool, error) {
		initFunc := oom.GetInitOOMPriorityBPFFunc()

		if initFunc == nil {
			general.Warningf("oom bpf init func isn't registered yet")
			return false, nil
		}

		err := initFunc()
		if err != nil {
			general.Errorf("initialize oom priority bpf failed with error: %v", err)
			return false, nil
		}

		p.oomPriorityMapLock.Lock()
		p.oomPriorityMap, err = ebpf.LoadPinnedMap(p.oomPriorityMapPinnedPath, &ebpf.LoadPinOptions{})
		if err != nil {
			p.oomPriorityMapLock.Unlock()
			general.Errorf("load oom priority map at: %s failed with error: %v", p.oomPriorityMapPinnedPath, err)
			return false, nil
		}
		p.oomPriorityMapLock.Unlock()

		return true, nil
	}, stopCh)

	if err != nil {
		general.Errorf("polling to initialize oom priority bpf failed with error: %v", err)
	}

	general.Infof("initialize oom priority bpf successfully")
}

func (p *DynamicPolicy) clearOOMPriority(_ context.Context, emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer, req interface{}, _ interface{}) error {
	realReq, ok := req.(*pluginapi.RemovePodRequest)
	if !ok || realReq == nil {
		general.Errorf("clearOOMPriority got invalid request")
		return nil
	}

	if p.oomPriorityMap == nil {
		general.Errorf("oom priority bpf has not been initialized yet")
		return nil
	}

	cgIDList, err := metaServer.ExternalManager.ListCgroupIDsForPod(realReq.PodUid)
	if err != nil {
		if general.IsErrNotFound(err) {
			general.Warningf("pod %s not found", realReq.PodUid)
			return nil
		}
		general.Errorf("[clearOOMPriority] failed to list cgroup ids for pod %s: %v", realReq.PodUid, err)
		return nil
	}

	p.oomPriorityMapLock.Lock()
	defer p.oomPriorityMapLock.Unlock()
	var wg sync.WaitGroup

	for _, cgID := range cgIDList {
		wg.Add(1)
		go func(cgID uint64) {
			err := p.oomPriorityMap.Delete(cgID)
			if err != nil && !general.IsErrKeyNotExist(err) {
				general.Errorf("[clearOOMPriority] failed to delete oom pinned map entry for pod %s cgroup id %d: %v", realReq.PodUid, cgID, err)
				_ = p.emitter.StoreInt64(util.MetricNameMemoryOOMPriorityDeleteFailed, 1,
					metrics.MetricTypeNameRaw, metrics.ConvertMapToTags(map[string]string{
						"pod_uid": realReq.PodUid,
						"cg_id":   strconv.FormatUint(cgID, 10),
					})...)
			}
			wg.Done()
		}(cgID)
	}
	wg.Wait()

	return nil
}
