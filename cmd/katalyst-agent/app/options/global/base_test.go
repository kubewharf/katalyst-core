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

package global

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentglobal "github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

func TestBaseOptionsApplyCompatibilityCheckpointDirectory(t *testing.T) {
	t.Parallel()

	options := NewBaseOptions()
	require.Equal(t, "/var/lib/katalyst/compatibility", options.CompatibilityCheckpointDirectory)

	options.CompatibilityCheckpointDirectory = ""
	configuration := agentglobal.NewBaseConfiguration()
	require.NoError(t, options.ApplyTo(configuration))
	require.Empty(t, configuration.CompatibilityCheckpointDirectory)
}
