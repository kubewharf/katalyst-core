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

package eviction

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestCPUPressureEvictionConfigurationApplyPIDOveruseConfiguration(t *testing.T) {
	t.Parallel()

	enablePIDOveruseEviction := true
	pidOveruseThreshold := int64(256)
	gracePeriod := int64(9)
	labelSelector := "tier=gold"
	annotationSelector := "salemode=reserved"

	conf := NewCPUPressureEvictionConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					EvictionConfig: &configapi.EvictionConfig{
						CPUPressureEvictionConfig: &configapi.CPUPressureEvictionConfig{
							LoadEvictionCoolDownTime: &metav1.Duration{},
						},
						PIDOveruseEvictionConfig: &configapi.PIDOveruseEvictionConfig{
							EnablePIDOveruseEviction:       &enablePIDOveruseEviction,
							PIDOveruseThreshold:            &pidOveruseThreshold,
							CandidatePodLabelSelector:      &labelSelector,
							CandidatePodAnnotationSelector: &annotationSelector,
							GracePeriod:                    &gracePeriod,
						},
					},
				},
			},
		},
	})

	require.True(t, conf.EnablePIDOveruseEviction)
	require.EqualValues(t, 256, conf.PIDOveruseThreshold)
	require.EqualValues(t, 9, conf.PIDOveruseGracePeriod)
	require.Equal(t, labelSelector, conf.PIDOveruseCandidatePodLabelSelector)
	require.Equal(t, annotationSelector, conf.PIDOveruseCandidatePodAnnotationSelector)
}
