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

package dynamic

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
)

// DynamicConfigCRD records all those configurations defined by CRD
// and managed by KCC, which field nme must be the same as the Kind
// of CRD. KCC components are responsible to identify those CRs and
// trigger notification.
type DynamicConfigCRD struct {
	AdminQoSConfiguration *v1alpha1.AdminQoSConfiguration
	EvictionConfiguration *v1alpha1.EvictionConfiguration
}

var (
	// AdminQoSConfigurationGVR is the group version resource for AdminQoSConfiguration
	AdminQoSConfigurationGVR = metav1.GroupVersionResource(v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.ResourceNameAdminQoSConfigurations))

	// EvictionConfigurationGVR is the group version resource for EvictionConfiguration
	EvictionConfigurationGVR = metav1.GroupVersionResource(v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.ResourceNameEvictionConfigurations))
)
