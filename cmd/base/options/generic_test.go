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
	"testing"

	"github.com/stretchr/testify/assert"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestGenericOptionsApplyToPodSaleModeAnnotationKey(t *testing.T) {
	t.Parallel()

	options := NewGenericOptions()
	assert.Equal(t, apiconsts.PodAnnotationSaleModeKey, options.PodSaleModeAnnotationKey)

	conf := generic.NewGenericConfiguration()
	err := options.ApplyTo(conf)

	assert.NoError(t, err)
	assert.Equal(t, apiconsts.PodAnnotationSaleModeKey, conf.PodSaleModeAnnotationKey)
	assert.Equal(t, apiconsts.PodAnnotationSaleModeKey, conf.GetPodSaleModeAnnotationKey())
}
