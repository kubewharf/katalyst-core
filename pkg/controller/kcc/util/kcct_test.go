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
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func toTestUnstructured(obj interface{}) *unstructured.Unstructured {
	ret, err := native.ToUnstructured(obj)
	if err != nil {
		klog.Error(err)
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

func generateTestTargetResource(name, labelSelector string, nodeNames []string) *unstructured.Unstructured {
	return toTestUnstructured(&apisv1alpha1.EvictionConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.EvictionConfigurationSpec{
			GenericConfigSpec: apisv1alpha1.GenericConfigSpec{
				NodeLabelSelector: labelSelector,
				EphemeralSelector: apisv1alpha1.EphemeralSelector{
					NodeNames: nodeNames,
				},
			},
		},
		Status: apisv1alpha1.GenericConfigStatus{},
	})
}

func generateTestTargetResourceWithTimeout(name, labelSelector string, nodeNames []string) *unstructured.Unstructured {
	return toTestUnstructured(&apisv1alpha1.EvictionConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Minute * 10)),
		},
		Spec: apisv1alpha1.EvictionConfigurationSpec{
			GenericConfigSpec: apisv1alpha1.GenericConfigSpec{
				NodeLabelSelector: labelSelector,
				EphemeralSelector: apisv1alpha1.EphemeralSelector{
					NodeNames:    nodeNames,
					LastDuration: &metav1.Duration{Duration: time.Minute},
				},
			},
		},
		Status: apisv1alpha1.GenericConfigStatus{},
	})
}

func Test_findMatchedTargetConfig(t *testing.T) {
	type args struct {
		cnc        *apisv1alpha1.CustomNodeConfig
		configList []*unstructured.Unstructured
	}
	tests := []struct {
		name    string
		args    args
		want    []*unstructured.Unstructured
		wantErr bool
	}{
		{
			name: "only nodeNames config",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResource("config-1", "", []string{"node-1"}),
					generateTestTargetResource("config-2", "", []string{"node-2"}),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResource("config-1", "", []string{"node-1"}),
			},
		},
		{
			name: "nodeNames overlap",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResource("config-1", "", []string{"node-1"}),
					generateTestTargetResource("config-2", "", []string{"node-1"}),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResource("config-1", "", []string{"node-1"}),
				generateTestTargetResource("config-2", "", []string{"node-1"}),
			},
		},
		{
			name: "target config invalid",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResourceWithTimeout("config-1", "aa=default", []string{"node-1"}),
					generateTestTargetResourceWithTimeout("config-2", "", nil),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResourceWithTimeout("config-1", "aa=default", []string{"node-1"}),
			},
		},
		{
			name: "nodeNames prior than labelSelector",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResource("config-1", "aa=default", nil),
					generateTestTargetResource("config-2", "", []string{"node-1"}),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResource("config-2", "", []string{"node-1"}),
			},
		},
		{
			name: "labelSelector overlap",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResource("config-1", "aa=default", nil),
					generateTestTargetResource("config-2", "", nil),
					generateTestTargetResource("config-3", "aa in (default,non-standard)", nil),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResource("config-1", "aa=default", nil),
				generateTestTargetResource("config-3", "aa in (default,non-standard)", nil),
			},
		},
		{
			name: "labelSelector overlap but nodeName valid",
			args: args{
				cnc: generateTestCNC("node-1", map[string]string{
					"aa": "default",
				}),
				configList: []*unstructured.Unstructured{
					generateTestTargetResource("config-1", "aa=default", nil),
					generateTestTargetResource("config-2", "", []string{"node-1"}),
					generateTestTargetResource("config-3", "aa in (default,non-standard)", nil),
				},
			},
			want: []*unstructured.Unstructured{
				generateTestTargetResource("config-2", "", []string{"node-1"}),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findMatchedKCCTargetConfigForNode(tt.args.cnc, tt.args.configList)
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
		t.Run(tt.name, func(t *testing.T) {
			if got := UpdateKCCTGenericConditions(tt.args.status, tt.args.conditionType, tt.args.conditionStatus, tt.args.reason, tt.args.message); got != tt.want {
				t.Errorf("UpdateKCCTGenericConditions() = %v, want %v", got, tt.want)
			}
		})
	}
}
