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

package userwatermark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	cliflag "k8s.io/component-base/cli/flag"

	dynamicuserwm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/userwatermark"
)

func TestNewUserWatermarkOptions(t *testing.T) {
	t.Parallel()

	opts := NewUserWatermarkOptions()
	assert.NotNil(t, opts)
	assert.NotNil(t, opts.DefaultOptions)
}

func TestUserWatermarkOptions_AddFlags(t *testing.T) {
	t.Parallel()

	opts := NewUserWatermarkOptions()
	fss := &cliflag.NamedFlagSets{}

	opts.AddFlags(fss)
	fs := fss.FlagSet("user-watermark")
	assert.NotNil(t, fs.Lookup("enable-user-watermark-reclaimer"))
	assert.NotNil(t, fs.Lookup("user-watermark-reconcile-interval"))
	assert.NotNil(t, fs.Lookup("user-watermark-pod-service-label"))
}

func TestUserWatermarkOptions_ApplyTo(t *testing.T) {
	t.Parallel()

	opts := &UserWatermarkOptions{
		EnableReclaimer:   true,
		ReconcileInterval: 10,
		ServiceLabel:      "service-label",
		DefaultOptions:    NewUserWatermarkOptions().DefaultOptions,
	}

	conf := dynamicuserwm.NewUserWatermarkConfiguration()
	err := opts.ApplyTo(conf)

	assert.NoError(t, err)
	assert.True(t, conf.EnableReclaimer)
	assert.Equal(t, int64(10), conf.ReconcileInterval)
	assert.Equal(t, "service-label", conf.ServiceLabel)
	assert.NotNil(t, conf.DefaultConfig)
}
