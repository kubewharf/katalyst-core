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

package kcc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestKatalystCustomConfigTargetController_Run(t *testing.T) {
	t.Parallel()

	type args struct {
		kccList       []runtime.Object
		kccTargetList []runtime.Object
		cncList       []runtime.Object
	}
	tests := []struct {
		name                  string
		args                  args
		expectedKCCTargetList []*v1alpha1.AdminQoSConfiguration
		expectedCNCList       []*v1alpha1.CustomNodeConfig
	}{
		{
			name: "kcc and kcc target are all valid",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "default",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 5.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 1.5,
										},
									},
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "aa-bb",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								NodeLabelSelector: "aa=bb",
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 6.0,
										},
									},
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation:        1,
							Name:              "node-1",
							Namespace:         "default",
							CreationTimestamp: metav1.Now(),
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								EphemeralSelector: v1alpha1.EphemeralSelector{
									NodeNames: []string{
										"node-1",
									},
									LastDuration: &metav1.Duration{
										Duration: 99999 * time.Hour,
									},
								},
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 15.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
									},
								},
							},
						},
					},
				},
				cncList: []runtime.Object{
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-1",
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-2",
							Labels: map[string]string{
								"aa": "bb",
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-3",
						},
					},
				},
			},
			expectedKCCTargetList: []*v1alpha1.AdminQoSConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "default",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 5.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 1.5,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        1,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "190b30322065",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "aa-bb",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 6.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        1,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "71cf0929a297",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation:        1,
						Name:              "node-1",
						Namespace:         "default",
						CreationTimestamp: metav1.Now(),
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							EphemeralSelector: v1alpha1.EphemeralSelector{
								NodeNames: []string{
									"node-1",
								},
								LastDuration: &metav1.Duration{
									Duration: 99999 * time.Hour,
								},
							},
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 15.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        1,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "d4b70d79b95f",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
			},
			expectedCNCList: []*v1alpha1.CustomNodeConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "node-1",
								Hash:            "d4b70d79b95f",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Labels: map[string]string{
							"aa": "bb",
						},
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "aa-bb",
								Hash:            "71cf0929a297",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-3",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "190b30322065",
							},
						},
					},
				},
			},
		},
		{
			name: "invalid kcc targets - retain old cnc hashes",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "default",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 5.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 1.5,
										},
									},
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "aa-bb",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								NodeLabelSelector: "aa=bb",
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 6.0,
										},
									},
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation:        1,
							Name:              "aa-in-bb",
							Namespace:         "default",
							CreationTimestamp: metav1.Now(),
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								NodeLabelSelector: "aa in (bb,cc)",
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 15.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
									},
								},
							},
						},
					},
				},
				cncList: []runtime.Object{
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-1",
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "default",
									Hash:            "someHash",
								},
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-2",
							Labels: map[string]string{
								"aa": "bb",
							},
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "default",
									Hash:            "anotherHash",
								},
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-3",
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "default",
									Hash:            "yetAnotherHash",
								},
							},
						},
					},
				},
			},
			expectedKCCTargetList: []*v1alpha1.AdminQoSConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "default",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 5.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 1.5,
									},
								},
							},
						},
					},
					// in the presence of other invalid targets, we cannot calculate the
					// number of target nodes accurately.
					Status: v1alpha1.GenericConfigStatus{},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "aa-bb",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 6.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:    v1alpha1.ConfigConditionTypeValid,
								Status:  v1.ConditionFalse,
								Reason:  kccTargetConditionReasonValidateFailed,
								Message: "labelSelector overlay with others: [default/aa-in-bb]",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation:        1,
						Name:              "aa-in-bb",
						Namespace:         "default",
						CreationTimestamp: metav1.Now(),
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa in (bb,cc)",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 15.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:    v1alpha1.ConfigConditionTypeValid,
								Status:  v1.ConditionFalse,
								Reason:  kccTargetConditionReasonValidateFailed,
								Message: "labelSelector overlay with others: [default/aa-bb]",
							},
						},
					},
				},
			},
			expectedCNCList: []*v1alpha1.CustomNodeConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "someHash",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Labels: map[string]string{
							"aa": "bb",
						},
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "anotherHash",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-3",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "yetAnotherHash",
							},
						},
					},
				},
			},
		},
		{
			name: "canary update",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "default",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 5.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 1.5,
										},
									},
								},
							},
						},
						Status: v1alpha1.GenericConfigStatus{
							TargetNodes:        1,
							CanaryNodes:        1,
							UpdatedTargetNodes: 1,
							UpdatedNodes:       1,
							CurrentHash:        "190b30322065",
							ObservedGeneration: 1,
							Conditions: []v1alpha1.GenericConfigCondition{
								{
									Type:   v1alpha1.ConfigConditionTypeValid,
									Status: v1.ConditionTrue,
									Reason: kccTargetConditionReasonNormal,
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 2,
							Name:       "aa-bb",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								NodeLabelSelector: "aa=bb",
								UpdateStrategy: v1alpha1.ConfigUpdateStrategy{
									Type: v1alpha1.RollingUpdateConfigStrategyType,
									RollingUpdate: &v1alpha1.RollingUpdateConfig{
										Canary: &intstr.IntOrString{
											Type:   intstr.String,
											StrVal: "50%",
										},
									},
								},
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 15.0,
										},
										SoftEvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
									},
								},
							},
						},
						Status: v1alpha1.GenericConfigStatus{
							TargetNodes:        2,
							CanaryNodes:        2,
							UpdatedTargetNodes: 2,
							UpdatedNodes:       2,
							CurrentHash:        "d4b70d79b95f",
							ObservedGeneration: 1,
							Conditions: []v1alpha1.GenericConfigCondition{
								{
									Type:   v1alpha1.ConfigConditionTypeValid,
									Status: v1.ConditionTrue,
									Reason: kccTargetConditionReasonNormal,
								},
							},
						},
					},
				},
				cncList: []runtime.Object{
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-1",
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "default",
									Hash:            "190b30322065",
								},
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-2",
							Labels: map[string]string{
								"aa": "bb",
							},
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "aa-bb",
									Hash:            "8c1738cd5743",
								},
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-3",
							Labels: map[string]string{
								"aa": "bb",
							},
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "aa-bb",
									Hash:            "8c1738cd5743",
								},
							},
						},
					},
				},
			},
			expectedKCCTargetList: []*v1alpha1.AdminQoSConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "default",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 5.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 1.5,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        1,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "190b30322065",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 2,
						Name:       "aa-bb",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
							UpdateStrategy: v1alpha1.ConfigUpdateStrategy{
								Type: v1alpha1.RollingUpdateConfigStrategyType,
								RollingUpdate: &v1alpha1.RollingUpdateConfig{
									Canary: &intstr.IntOrString{
										Type:   intstr.String,
										StrVal: "50%",
									},
								},
							},
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 15.0,
									},
									SoftEvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        2,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "d4b70d79b95f",
						ObservedGeneration: 2,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
			},
			expectedCNCList: []*v1alpha1.CustomNodeConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "190b30322065",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Labels: map[string]string{
							"aa": "bb",
						},
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "aa-bb",
								Hash:            "d4b70d79b95f",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-3",
						Labels: map[string]string{
							"aa": "bb",
						},
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "aa-bb",
								Hash:            "8c1738cd5743",
							},
						},
					},
				},
			},
		},

		{
			name: "target config deletion",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 1,
							Name:       "default",
							Namespace:  "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 5.0,
										},
									},
								},
							},
						},
						Status: v1alpha1.GenericConfigStatus{
							TargetNodes:        1,
							CanaryNodes:        1,
							UpdatedTargetNodes: 1,
							UpdatedNodes:       1,
							CurrentHash:        "d66c6afd9fc2",
							ObservedGeneration: 1,
							Conditions: []v1alpha1.GenericConfigCondition{
								{
									Type:   v1alpha1.ConfigConditionTypeValid,
									Status: v1.ConditionTrue,
									Reason: kccTargetConditionReasonNormal,
								},
							},
						},
					},
					&v1alpha1.AdminQoSConfiguration{
						ObjectMeta: metav1.ObjectMeta{
							Generation: 2,
							Name:       "aa-bb",
							Namespace:  "default",
							Finalizers: []string{
								consts.KatalystCustomConfigTargetFinalizerKCCT,
								consts.KatalystCustomConfigTargetFinalizerCNC,
							},
							DeletionTimestamp: &metav1.Time{Time: time.Now()},
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							GenericConfigSpec: v1alpha1.GenericConfigSpec{
								NodeLabelSelector: "aa=bb",
							},
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
										EvictionThreshold: map[v1.ResourceName]float64{
											v1.ResourceCPU: 10.0,
										},
									},
								},
							},
						},
						Status: v1alpha1.GenericConfigStatus{
							TargetNodes:        1,
							CanaryNodes:        1,
							UpdatedTargetNodes: 1,
							UpdatedNodes:       1,
							CurrentHash:        "8c1738cd5743",
							ObservedGeneration: 1,
							Conditions: []v1alpha1.GenericConfigCondition{
								{
									Type:   v1alpha1.ConfigConditionTypeValid,
									Status: v1.ConditionTrue,
									Reason: kccTargetConditionReasonNormal,
								},
							},
						},
					},
				},
				cncList: []runtime.Object{
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-1",
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "default",
									Hash:            "d66c6afd9fc2",
								},
							},
						},
					},
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node-2",
							Labels: map[string]string{
								"aa": "bb",
							},
						},
						Status: v1alpha1.CustomNodeConfigStatus{
							KatalystCustomConfigList: []v1alpha1.TargetConfig{
								{
									ConfigType:      crd.AdminQoSConfigurationGVR,
									ConfigNamespace: "default",
									ConfigName:      "aa-bb",
									Hash:            "8c1738cd5743",
								},
							},
						},
					},
				},
			},
			expectedKCCTargetList: []*v1alpha1.AdminQoSConfiguration{
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation: 1,
						Name:       "default",
						Namespace:  "default",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 5.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        2,
						CanaryNodes:        2,
						UpdatedTargetNodes: 2,
						UpdatedNodes:       2,
						CurrentHash:        "d66c6afd9fc2",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
				// the terminating object cannot be deleted in the fake client, but
				// its finalizers should be emptied and no nodes should continue to use its config.
				{
					ObjectMeta: metav1.ObjectMeta{
						Generation:        2,
						Name:              "aa-bb",
						Namespace:         "default",
						DeletionTimestamp: &metav1.Time{Time: time.Now()},
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
						Config: v1alpha1.AdminQoSConfig{
							EvictionConfig: &v1alpha1.EvictionConfig{
								ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
									EvictionThreshold: map[v1.ResourceName]float64{
										v1.ResourceCPU: 10.0,
									},
								},
							},
						},
					},
					Status: v1alpha1.GenericConfigStatus{
						TargetNodes:        1,
						CanaryNodes:        1,
						UpdatedTargetNodes: 1,
						UpdatedNodes:       1,
						CurrentHash:        "8c1738cd5743",
						ObservedGeneration: 1,
						Conditions: []v1alpha1.GenericConfigCondition{
							{
								Type:   v1alpha1.ConfigConditionTypeValid,
								Status: v1.ConditionTrue,
								Reason: kccTargetConditionReasonNormal,
							},
						},
					},
				},
			},
			expectedCNCList: []*v1alpha1.CustomNodeConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "d66c6afd9fc2",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-2",
						Labels: map[string]string{
							"aa": "bb",
						},
					},
					Status: v1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []v1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "default",
								Hash:            "d66c6afd9fc2",
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			assertor := assert.New(t)
			t.Parallel()

			for _, list := range [][]runtime.Object{tt.args.kccList, tt.args.kccTargetList, tt.args.cncList} {
				for _, obj := range list {
					kind := reflect.TypeOf(obj).Elem().Name()
					gvk := v1alpha1.SchemeGroupVersion.WithKind(kind)
					obj.GetObjectKind().SetGroupVersionKind(gvk)
				}
			}

			genericContext, err := katalyst_base.GenerateFakeGenericContext(
				nil,
				append(tt.args.kccList, tt.args.cncList...),
				tt.args.kccTargetList,
			)
			assertor.NoError(err)
			conf := generateTestConfiguration(t)

			ctx := context.Background()
			targetHandler := kcctarget.NewKatalystCustomConfigTargetHandler(
				ctx,
				genericContext.Client,
				conf.ControllersConfiguration.KCCConfig,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
			)

			targetController, err := NewKatalystCustomConfigTargetController(
				ctx,
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.KCCConfig,
				genericContext.Client,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
				genericContext.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
				metrics.DummyMetrics{},
				targetHandler,
			)
			assertor.NoError(err)
			// minimize the delay durations
			targetController.cncEnqueueDelay = 0
			targetController.kcctEnqueueDelay = 0
			targetController.cncUpdateQPS = 100
			targetController.cncUpdateBurst = 1000

			genericContext.StartInformer(ctx)
			go targetHandler.Run()
			go targetController.Run()

			synced := cache.WaitForCacheSync(targetController.ctx.Done(), targetController.syncedFunc...)
			assertor.True(synced)

			assertor.EventuallyWithT(func(c *assert.CollectT) {
				assertor := assert.New(c)
				t.Logf("Checking the status of the objects")

				for _, expectedTargetObject := range tt.expectedKCCTargetList {
					kind := reflect.TypeOf(expectedTargetObject).Elem().Name()
					gvk := v1alpha1.SchemeGroupVersion.WithKind(kind)
					expectedTargetObject.SetGroupVersionKind(gvk)

					gvr := v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.ResourceNameAdminQoSConfigurations)
					actualUns, err := genericContext.Client.DynamicClient.Resource(gvr).Namespace(expectedTargetObject.GetNamespace()).Get(ctx, expectedTargetObject.GetName(), metav1.GetOptions{})
					assertor.NoError(err)

					expectedUns, err := runtime.DefaultUnstructuredConverter.ToUnstructured(expectedTargetObject)
					assertor.NoError(err)
					actualUns = convertFromAndToUnstructured(actualUns)

					assertor.Equal(expectedUns, actualUns.Object)
				}

				for _, expectedCNCObject := range tt.expectedCNCList {
					kind := reflect.TypeOf(expectedCNCObject).Elem().Name()
					gvk := v1alpha1.SchemeGroupVersion.WithKind(kind)
					expectedCNCObject.SetGroupVersionKind(gvk)

					actualObject, err := genericContext.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().Get(ctx, expectedCNCObject.GetName(), metav1.GetOptions{})
					assertor.NoError(err)
					assertor.Equal(expectedCNCObject, actualObject)
				}
			}, time.Second*5, time.Millisecond*500)
		})
	}
}

// We have to do this to get consistent results, e.g. convert empty slices field that are `omitempty` to nil.
func convertFromAndToUnstructured(obj *unstructured.Unstructured) *unstructured.Unstructured {
	typed := &v1alpha1.AdminQoSConfiguration{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, typed)
	if err != nil {
		panic(err)
	}
	for i := range typed.Status.Conditions {
		typed.Status.Conditions[i].LastTransitionTime = metav1.Time{}
	}
	newObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: newObj}
}
