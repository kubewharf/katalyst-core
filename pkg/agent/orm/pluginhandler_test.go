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

package orm

import (
	"testing"

	"github.com/stretchr/testify/require"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
)

func TestDeRegisterPlugin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		registeredEndpoint string
		deletedEndpoint    string
		wantStopped        bool
	}{
		{
			name:               "matching endpoint",
			registeredEndpoint: "/var/lib/kubelet/plugins/resource.sock",
			deletedEndpoint:    "/var/lib/kubelet/plugins/resource.sock",
			wantStopped:        true,
		},
		{
			name:               "stale endpoint",
			registeredEndpoint: "/var/lib/kubelet/plugins/resource-new.sock",
			deletedEndpoint:    "/var/lib/kubelet/plugins/resource-old.sock",
			wantStopped:        false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &MockEndpoint{socketPath: tc.registeredEndpoint}
			m := &ManagerImpl{
				endpoints: map[string]endpoint.EndpointInfo{
					"resource": {
						E:    e,
						Opts: &pluginapi.ResourcePluginOptions{},
					},
				},
			}

			m.DeRegisterPlugin("resource", tc.deletedEndpoint)

			require.Equal(t, tc.wantStopped, e.IsStopped())
		})
	}
}
