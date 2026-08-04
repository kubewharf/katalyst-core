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

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func TestNewCPUOptions_Defaults_ContainerCPUIdle(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewCPUOptions()

	as.False(o.EnableContainerCPUIdle)
}

func TestCPUOptions_AddFlags_ParseContainerCPUIdle(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewCPUOptions()

	fss := cliflag.NamedFlagSets{}
	o.AddFlags(&fss)
	fs := fss.FlagSet("cpu_resource_plugin")

	as.NotNil(fs.Lookup("enable-container-cpu-idle"))
	as.NoError(fs.Parse([]string{"--enable-container-cpu-idle=true"}))
	as.True(o.EnableContainerCPUIdle)
}

func TestCPUOptions_ApplyTo_CopiesContainerCPUIdle(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewCPUOptions()
	o.EnableContainerCPUIdle = true

	conf := qrmconfig.NewCPUQRMPluginConfig()
	as.NoError(o.ApplyTo(conf))
	as.True(conf.EnableContainerCPUIdle)
}
