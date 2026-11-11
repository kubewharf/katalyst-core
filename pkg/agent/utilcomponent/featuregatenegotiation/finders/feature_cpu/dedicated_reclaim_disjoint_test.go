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

package feature_cpu

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

func TestDedicatedReclaimDisjointPartition_GetFeatureGate(t *testing.T) {
	t.Parallel()

	finder := &DedicatedReclaimDisjointPartition{}

	disabled := config.NewConfiguration()
	require.Nil(t, finder.GetFeatureGate(disabled))
	require.Nil(t, finder.GetFeatureGate(nil))

	enabled := config.NewConfiguration()
	enabled.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true
	got := finder.GetFeatureGate(enabled)
	require.Equal(t, NegotiationFeatureGateDedicatedReclaimDisjointPartition, got.Name)
	require.Equal(t, finders.FeatureGateTypeCPU, got.Type)
	require.True(t, got.MustMutuallySupported)
}
