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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestIsSharedPod(t *testing.T) {
	t.Parallel()

	SetQoSConfig(generic.NewQoSConfiguration())

	sharedPod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
		},
	}
	assert.True(t, IsSharedPod(sharedPod))

	notSharedPod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
			},
		},
	}
	assert.False(t, IsSharedPod(notSharedPod))
}

func TestGetQosLevelForPod(t *testing.T) {
	t.Parallel()

	SetQoSConfig(generic.NewQoSConfiguration())

	sharedPod := &v1.Pod{
		ObjectMeta: v12.ObjectMeta{
			Annotations: map[string]string{
				consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
			},
		},
	}
	qosLevel, err := GetQosLevelForPod(sharedPod)
	assert.NoError(t, err)
	assert.Equal(t, consts.PodAnnotationQoSLevelSharedCores, qosLevel)
}
