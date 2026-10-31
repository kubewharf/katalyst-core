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

package region

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

// newTestShareRegionForEnableReclaim builds a share region owned by "my-pool"
// with a real conf/metaServer/metaCache, mirroring TestIsNumaBinding.
func newTestShareRegionForEnableReclaim(t *testing.T) *QoSRegionShare {
	t.Helper()
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)
	conf.GenericSysAdvisorConfiguration.StateFileDirectory = t.TempDir()
	conf.MetaServerConfiguration.CheckpointManagerDir = t.TempDir()
	conf.CheckpointManagerDir = t.TempDir()

	genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{})
	require.NoError(t, err)
	metaServer, err := metaserver.NewMetaServer(genericCtx.Client, metrics.DummyMetrics{}, conf)
	require.NoError(t, err)

	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)

	ci := types.ContainerInfo{
		QoSLevel:            "shared_cores",
		OriginOwnerPoolName: "my-pool",
	}
	return NewQoSRegionShare(&ci, conf, nil, commonstate.FakedNUMAID, metaCache, metaServer, metrics.DummyMetrics{}).(*QoSRegionShare)
}

// TestQoSRegionShareEnableReclaimInternalExternalConsistent pins the C9 contract:
// a share pool listed in DisableReclaimSharePools must be treated as reclaim-disabled
// by BOTH the public EnableReclaim() and the internal provision-policy view
// (GetProvisionPolicy, which reads the r.enableReclaim closure under lock).
func TestQoSRegionShareEnableReclaimInternalExternalConsistent(t *testing.T) {
	t.Parallel()

	share := newTestShareRegionForEnableReclaim(t)
	dynamicConf := share.conf.GetDynamicConfiguration()

	// Case 1: pool disabled -> external and internal views both report reclaim off.
	dynamicConf.DisableReclaimSharePools = []string{"my-pool"}
	share.SetEssentials(types.ResourceEssentials{
		DynamicConfiguration: dynamicConf,
		EnableReclaim:        true,
	})

	require.False(t, share.EnableReclaim(),
		"public EnableReclaim must be false when the owner pool is disabled")
	_, policyInUse := share.GetProvisionPolicy()
	require.Equal(t, types.CPUProvisionPolicyNonReclaim, policyInUse,
		"internal provision view must treat a disabled pool as non-reclaim")

	// Case 2: pool not disabled -> both views report reclaim on.
	dynamicConf.DisableReclaimSharePools = nil
	share.SetEssentials(types.ResourceEssentials{
		DynamicConfiguration: dynamicConf,
		EnableReclaim:        true,
	})

	require.True(t, share.EnableReclaim(),
		"public EnableReclaim must be true when the owner pool is not disabled")
	_, policyInUse = share.GetProvisionPolicy()
	require.NotEqual(t, types.CPUProvisionPolicyNonReclaim, policyInUse,
		"internal provision view must not force non-reclaim when the pool is enabled")
}
