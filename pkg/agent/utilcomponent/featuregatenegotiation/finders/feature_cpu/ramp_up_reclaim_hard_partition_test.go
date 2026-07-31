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

package feature_cpu

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

func TestCPURampUpReclaimHardPartition_GetFeatureGate(t *testing.T) {
	t.Parallel()

	e := &CPURampUpReclaimHardPartition{}

	t.Run("enabled returns must-supported feature gate", func(t *testing.T) {
		t.Parallel()

		conf := config.NewConfiguration()
		conf.GetDynamicConfiguration().EnableRampUpReclaimHardPartition = true

		fg := e.GetFeatureGate(conf)
		if assert.NotNil(t, fg) {
			assert.Equal(t, NegotiationFeatureGateCPURampUpReclaimHardPartition, fg.Name)
			assert.Equal(t, finders.FeatureGateTypeCPU, fg.Type)
			assert.True(t, fg.MustMutuallySupported)
		}
	})

	t.Run("disabled returns nil", func(t *testing.T) {
		t.Parallel()

		conf := config.NewConfiguration()
		assert.Nil(t, e.GetFeatureGate(conf))
	})

	t.Run("nil config returns nil", func(t *testing.T) {
		t.Parallel()

		assert.Nil(t, e.GetFeatureGate(nil))
	})
}
