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

package metaserver

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

// TestMetaServerOptions_ApplyTo_RejectsNonPositivePodCacheSyncRateAndBurst pins the
// boundary contract: ApplyTo must reject non-positive kubelet-pod-cache-sync-max-rate
// and kubelet-pod-cache-sync-burst-bulk instead of silently feeding them into the
// token-bucket rate limiter built by the pod consumer
// (rate.NewLimiter(KubeletPodCacheSyncMaxRate, KubeletPodCacheSyncBurstBulk)).
//
// RED: before the boundary check, ApplyTo returned nil for every case below.
// The zero cases (rate=0, burst=0) must also error so that a mutation of the
// production guard from `<= 0` to `< 0` (which would re-allow zero) is killed.
func TestMetaServerOptions_ApplyTo_RejectsNonPositivePodCacheSyncRateAndBurst(t *testing.T) {
	t.Parallel()
	valid := NewMetaServerOptions()
	require.NoError(t, valid.ApplyTo(agentconfig.NewMetaServerConfiguration()),
		"defaults must stay accepted")

	cases := []struct {
		name  string
		rate  int
		burst int
	}{
		{name: "rate zero", rate: 0, burst: 1},
		{name: "rate negative", rate: -1, burst: 1},
		{name: "burst zero", rate: 5, burst: 0},
		{name: "burst negative", rate: 5, burst: -1},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := NewMetaServerOptions()
			o.KubeletPodCacheSyncMaxRate = tc.rate
			o.KubeletPodCacheSyncBurstBulk = tc.burst

			err := o.ApplyTo(agentconfig.NewMetaServerConfiguration())
			require.Errorf(t, err, "ApplyTo must reject %s instead of accepting it", tc.name)
		})
	}
}
