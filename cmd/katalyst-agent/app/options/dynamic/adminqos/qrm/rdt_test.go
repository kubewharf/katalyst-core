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

package qrm

import (
	"testing"

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

func TestDynamicRDTOptionsSeedsOnlyDynamicConfig(t *testing.T) {
	t.Parallel()

	options := NewDynamicRDTOptions()
	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)

	require.NotNil(t, fss.FlagSet("dynamic-qrm").Lookup("disable-rdt"))
	require.Nil(t, fss.FlagSet("resctrl").Lookup("disable-rdt"))
	require.NoError(t, fss.FlagSet("dynamic-qrm").Parse([]string{"--disable-rdt=true"}))

	conf := qrm.NewQRMPluginConfiguration()
	require.NoError(t, options.ApplyTo(conf))
	require.True(t, conf.RDTConfig.DisableRDT)
}
