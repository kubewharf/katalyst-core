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

func TestNewMemoryOptions_Defaults_LogCache(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()

	as.False(o.EnableEvictingLogCache)
	as.Equal(uint64(30), o.HighThreshold)
	as.Equal(uint64(5), o.LowThreshold)
}

func TestMemoryOptions_AddFlags_ParseLogCache(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()

	fss := cliflag.NamedFlagSets{}
	o.AddFlags(&fss)
	fs := fss.FlagSet("memory_resource_plugin")

	as.NotNil(fs.Lookup("enable-evicting-logcache"))
	as.NotNil(fs.Lookup("qrm-memory-logcache-high-threshold"))
	as.NotNil(fs.Lookup("qrm-memory-logcache-low-threshold"))

	as.NoError(fs.Parse([]string{
		"--enable-evicting-logcache=true",
		"--qrm-memory-logcache-high-threshold=123",
		"--qrm-memory-logcache-low-threshold=12",
	}))

	as.True(o.EnableEvictingLogCache)
	as.Equal(uint64(123), o.HighThreshold)
	as.Equal(uint64(12), o.LowThreshold)
}

func TestMemoryOptions_ApplyTo_CopiesLogCache(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()
	o.EnableEvictingLogCache = true
	o.HighThreshold = 333
	o.LowThreshold = 42

	conf := qrmconfig.NewMemoryQRMPluginConfig()
	as.NoError(o.ApplyTo(conf))

	as.True(conf.EnableEvictingLogCache)
	as.Equal(uint64(333), conf.HighThreshold)
	as.Equal(uint64(42), conf.LowThreshold)
}
