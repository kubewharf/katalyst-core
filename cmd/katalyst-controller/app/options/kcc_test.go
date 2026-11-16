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

package options

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"
)

func TestKCCOptions(t *testing.T) {
	t.Parallel()

	t.Run("default", func(t *testing.T) {
		t.Parallel()

		options := NewKCCOptions()
		config, err := options.Config()
		require.NoError(t, err)
		assert.Equal(t, 50, config.CNCUpdateQPS)
		assert.Equal(t, 100, config.CNCUpdateBurst)
	})

	t.Run("flag override", func(t *testing.T) {
		t.Parallel()

		options := NewKCCOptions()
		fss := &cliflag.NamedFlagSets{}
		options.AddFlags(fss)
		require.NoError(t, fss.FlagSets["kcc"].Parse([]string{
			"--kcc-cnc-update-qps=25",
			"--kcc-cnc-update-burst=200",
		}))

		config, err := options.Config()
		require.NoError(t, err)
		assert.Equal(t, 25, config.CNCUpdateQPS)
		assert.Equal(t, 200, config.CNCUpdateBurst)
	})

	for _, qps := range []int{0, -1} {
		qps := qps
		t.Run(fmt.Sprintf("invalid QPS %d", qps), func(t *testing.T) {
			t.Parallel()

			options := NewKCCOptions()
			options.CNCUpdateQPS = qps
			_, err := options.Config()
			assert.EqualError(t, err, "kcc-cnc-update-qps must be greater than 0")
		})
	}

	for _, burst := range []int{0, -1} {
		burst := burst
		t.Run(fmt.Sprintf("invalid burst %d", burst), func(t *testing.T) {
			t.Parallel()

			options := NewKCCOptions()
			options.CNCUpdateBurst = burst
			_, err := options.Config()
			assert.EqualError(t, err, "kcc-cnc-update-burst must be greater than 0")
		})
	}
}
