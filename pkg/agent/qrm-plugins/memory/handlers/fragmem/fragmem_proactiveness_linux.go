//go:build linux
// +build linux

/*
Copyright 2026 The Katalyst Authors.

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

package fragmem

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/util/errors"

	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	procfs "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

// SetVMCompactionProactiveness applies the current dynamic target and always observes the host value.
// It runs independently of the FragMem enable flag and compaction backoff.
func SetVMCompactionProactiveness(_ *coreconfig.Configuration,
	_ interface{}, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, _ *metaserver.MetaServer,
) {
	err := setVMCompactionProactivenessAtPath(dynamicConf, hostCompactProactivenessFile, emitter)
	if err != nil {
		general.Errorf("sync vm.compaction_proactiveness failed: %v", err)
	}
	_ = general.UpdateHealthzStateByError(memconsts.SetVMCompactionProactiveness, err)
}

func setVMCompactionProactivenessAtPath(dynamicConf *dynamicconfig.DynamicAgentConfiguration, path string, emitter metrics.MetricEmitter) error {
	var target *int64
	if conf := getFragMemConfiguration(dynamicConf); conf != nil {
		target = conf.VMCompactionProactiveness
	}
	return syncVMCompactionProactiveness(path, target, emitter)
}

func syncVMCompactionProactiveness(path string, target *int64, emitter metrics.MetricEmitter) error {
	var errList []error
	if target != nil {
		if err := procfs.ApplyVMCompactionProactivenessAtPath(path, *target); err != nil {
			errList = append(errList, fmt.Errorf("write vm.compaction_proactiveness: %w", err))
		}
	}

	// Read even after a failed write so the metric reflects the actual kernel value.
	data, err := os.ReadFile(path)
	if err != nil {
		// Older kernels may not expose this knob. Keep observation optional unless configured.
		if target == nil && os.IsNotExist(err) {
			return nil
		}
		return k8serrors.NewAggregate(append(errList, fmt.Errorf("read vm.compaction_proactiveness: %w", err)))
	}
	actual, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || actual < 0 || actual > 100 {
		return k8serrors.NewAggregate(append(errList, fmt.Errorf("invalid vm.compaction_proactiveness value %q", strings.TrimSpace(string(data)))))
	}

	if emitter != nil {
		_ = emitter.StoreInt64(metricNameVMCompactionProactiveness, actual, metrics.MetricTypeNameRaw)
	}
	if target != nil && actual != *target && len(errList) == 0 {
		errList = append(errList, fmt.Errorf("vm.compaction_proactiveness readback %d differs from target %d", actual, *target))
	}
	return k8serrors.NewAggregate(errList)
}
