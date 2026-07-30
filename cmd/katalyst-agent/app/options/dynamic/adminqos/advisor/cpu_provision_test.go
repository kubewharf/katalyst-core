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

package advisor

import (
	"testing"

	"github.com/stretchr/testify/require"

	advisorconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/advisor"
)

func TestCPUProvisionOptionsApplyToPropagatesDedicatedOverlapFlag(t *testing.T) {
	t.Parallel()

	options := NewCPUProvisionOptions()
	options.DisableDedicatedCoresOverlapReclaimedCores = true
	configuration := advisorconfig.NewCPUProvisionConfiguration()

	require.NoError(t, options.ApplyTo(configuration))
	require.True(t, configuration.DisableDedicatedCoresOverlapReclaimedCores)
}
