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

package metric

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/cgroup"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func init() {
	RegisterProvisioners(metaserver.MetricProvisionerMalachite, malachite.NewMalachiteMetricsProvisioner)
	RegisterProvisioners(metaserver.MetricProvisionerKubelet, kubelet.NewKubeletSummaryProvisioner)
	RegisterProvisioners(metaserver.MetricProvisionerCgroup, cgroup.NewCGroupMetricsProvisioner)
}

type ProvisionerInitFunc func(baseConf *global.BaseConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore) types.MetricsProvisioner

// provisioners stores the initializing function for each-provisioner
var provisioners sync.Map

// RegisterProvisioners registers user-defined resource plugin init functions
func RegisterProvisioners(name string, initFunc ProvisionerInitFunc) {
	provisioners.Store(name, initFunc)
}

// getProvisioners returns provisioner with initialized functions
func getProvisioners() map[string]ProvisionerInitFunc {
	results := make(map[string]ProvisionerInitFunc)
	provisioners.Range(func(key, value interface{}) bool {
		results[key.(string)] = value.(ProvisionerInitFunc)
		return true
	})
	return results
}
