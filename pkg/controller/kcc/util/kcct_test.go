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
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	apisv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func toTestUnstructured(obj interface{}) *unstructured.Unstructured {
	ret, err := native.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	return ret
}

func generateTestCNC(name string, labels map[string]string) *apisv1alpha1.CustomNodeConfig {
	return &apisv1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func generateTestTargetResource(
	namespace string,
	name string,
	labelSelector string,
	nodeNames []string,
	lastDuration *metav1.Duration,
) *unstructured.Unstructured {
	obj := &apisv1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: apisv1alpha1.AdminQoSConfigurationSpec{
			GenericConfigSpec: apisv1alpha1.GenericConfigSpec{
				NodeLabelSelector: labelSelector,
				EphemeralSelector: apisv1alpha1.EphemeralSelector{
					NodeNames: nodeNames,
				},
			},
		},
		Status: apisv1alpha1.GenericConfigStatus{},
	}
	if lastDuration != nil {
		obj.Spec.EphemeralSelector.LastDuration = lastDuration
	}
	return toTestUnstructured(obj)
}

func toTargetResources(configs ...*unstructured.Unstructured) []util.KCCTargetResource {
	ret := make([]util.KCCTargetResource, 0, len(configs))
	for _, config := range configs {
		ret = append(ret, util.KCCTargetResource{
			Unstructured: config,
		})
	}
	return ret
}

func TestIsCNCUpdated(t *testing.T) {
	t.Parallel()

	type args struct {
		cnc            *apisv1alpha1.CustomNodeConfig
		gvr            metav1.GroupVersionResource
		targetResource *unstructured.Unstructured
		hash           string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "same target present and updated",
			args: args{
				cnc: &apisv1alpha1.CustomNodeConfig{
					Status: apisv1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []apisv1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "config-1",
								Hash:            "sameHash",
							},
						},
					},
				},
				gvr:            crd.AdminQoSConfigurationGVR,
				targetResource: generateTestTargetResource("default", "config-1", "", nil, nil),
				hash:           "sameHash",
			},
			want: true,
		},
		{
			name: "same target present but outdated",
			args: args{
				cnc: &apisv1alpha1.CustomNodeConfig{
					Status: apisv1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []apisv1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "config-1",
								Hash:            "oldHash",
							},
						},
					},
				},
				gvr:            crd.AdminQoSConfigurationGVR,
				targetResource: generateTestTargetResource("default", "config-1", "", nil, nil),
				hash:           "newHash",
			},
			want: false,
		},
		{
			name: "different config present",
			args: args{
				cnc: &apisv1alpha1.CustomNodeConfig{
					Status: apisv1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []apisv1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "config-2",
								Hash:            "sameHash",
							},
						},
					},
				},
				gvr:            crd.AdminQoSConfigurationGVR,
				targetResource: generateTestTargetResource("default", "config-1", "", nil, nil),
				hash:           "sameHash",
			},
			want: false,
		},
		{
			name: "config gvr not found",
			args: args{
				cnc: &apisv1alpha1.CustomNodeConfig{
					Status: apisv1alpha1.CustomNodeConfigStatus{
						KatalystCustomConfigList: []apisv1alpha1.TargetConfig{
							{
								ConfigType:      crd.AdminQoSConfigurationGVR,
								ConfigNamespace: "default",
								ConfigName:      "config-1",
								Hash:            "someHash",
							},
						},
					},
				},
				gvr:            crd.AuthConfigurationGVR,
				targetResource: generateTestTargetResource("default", "testName", "", nil, nil),
				hash:           "newHash",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsCNCUpdated(tt.args.cnc, tt.args.gvr, util.ToKCCTargetResource(tt.args.targetResource), tt.args.hash); got != tt.want {
				t.Errorf("IsCNCUpdated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_findMatchedTargetConfig(t *testing.T) {
	t.Parallel()

	type args struct {
		cnc        *apisv1alpha1.CustomNodeConfig
		configList []util.KCCTargetResource
	}
	tests := []struct {
		name    string
		args    args
		want    []util.KCCTargetResource
		wantErr bool
	}{
		{
			name: "only nodeNames config",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "", []string{"node-1"}, nil),
					generateTestTargetResource("default", "config-2", "", []string{"node-2"}, nil),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-1", "", []string{"node-1"}, nil),
			),
		},
		{
			name: "nodeNames overlap",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "", []string{"node-1"}, nil),
					generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-1", "", []string{"node-1"}, nil),
				generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
			),
		},
		{
			name: "target config invalid",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "aa=default", []string{"node-1"}, &metav1.Duration{Duration: time.Minute}),
					generateTestTargetResource("default", "config-2", "", nil, &metav1.Duration{Duration: time.Minute}),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-1", "aa=default", []string{"node-1"}, &metav1.Duration{Duration: time.Minute}),
			),
		},
		{
			name: "nodeNames matched before labelSelector",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "aa=default", nil, nil),
					generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
			),
		},
		{
			name: "labelSelector overlap",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "aa=default", nil, nil),
					generateTestTargetResource("default", "config-2", "", nil, nil),
					generateTestTargetResource("default", "config-3", "aa in (default,non-standard)", nil, nil),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-1", "aa=default", nil, nil),
				generateTestTargetResource("default", "config-3", "aa in (default,non-standard)", nil, nil),
			),
		},
		{
			name: "labelSelector overlap but nodeName valid",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: toTargetResources(
					generateTestTargetResource("default", "config-1", "aa=default", nil, nil),
					generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
					generateTestTargetResource("default", "config-3", "aa in (default,non-standard)", nil, nil),
				),
			},
			want: toTargetResources(
				generateTestTargetResource("default", "config-2", "", []string{"node-1"}, nil),
			),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := findMatchedKCCTargetListForNode(tt.args.cnc, tt.args.configList)
			if (err != nil) != tt.wantErr {
				t.Errorf("findMatchedKCCTargetConfigForNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findMatchedKCCTargetConfigForNode() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUpdateKCCTGenericConditions(t *testing.T) {
	t.Parallel()

	type args struct {
		status          *apisv1alpha1.GenericConfigStatus
		conditionType   apisv1alpha1.ConfigConditionType
		conditionStatus v1.ConditionStatus
		reason          string
		message         string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "test-1",
			args: args{
				status:          &apisv1alpha1.GenericConfigStatus{},
				conditionType:   apisv1alpha1.ConfigConditionTypeValid,
				conditionStatus: v1.ConditionTrue,
				reason:          "",
				message:         "",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := UpdateKCCTGenericConditions(tt.args.status, tt.args.conditionType, tt.args.conditionStatus, tt.args.reason, tt.args.message); got != tt.want {
				t.Errorf("UpdateKCCTGenericConditions() = %v, want %v", got, tt.want)
			}
		})
	}
}
