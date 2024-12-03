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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
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

func testLabelSelector(t *testing.T, labelSelector string) labels.Selector {
	parse, err := labels.Parse(labelSelector)
	if err != nil {
		t.Fatal(err)
	}
	return parse
}

func generateTestLabelSelectorTargetResource(name, labelSelector string, priority int32) util.KCCTargetResource {
	return util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.AdminQoSConfigurationSpec{
			GenericConfigSpec: v1alpha1.GenericConfigSpec{
				NodeLabelSelector: labelSelector,
				Priority:          priority,
			},
		},
	}))
}

func generateTestNodeNamesTargetResource(name string, nodeNames []string) util.KCCTargetResource {
	return util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.AdminQoSConfigurationSpec{
			GenericConfigSpec: v1alpha1.GenericConfigSpec{
				EphemeralSelector: v1alpha1.EphemeralSelector{
					NodeNames: nodeNames,
				},
			},
		},
	}))
}

func Test_validateLabelSelectorWithOthers(t *testing.T) {
	t.Parallel()

	type args struct {
		priorityAllowedKeyListMap map[int32]sets.String
		targetResource            util.KCCTargetResource
		otherResources            []util.KCCTargetResource
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "test-1",
			args: args{
				priorityAllowedKeyListMap: map[int32]sets.String{
					0: sets.NewString("aa"),
				},
				targetResource: generateTestLabelSelectorTargetResource("1", "aa=bb", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("2", "aa=cc", 0),
				},
			},
			want: true,
		},
		{
			name: "test-2",
			args: args{
				priorityAllowedKeyListMap: map[int32]sets.String{
					0: sets.NewString("aa"),
				},
				targetResource: generateTestLabelSelectorTargetResource("1", "aa=bb", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("2", "aa in (cc,dd)", 0),
				},
			},
			want: true,
		},
		{
			name: "test-3",
			args: args{
				priorityAllowedKeyListMap: map[int32]sets.String{
					0: sets.NewString("aa"),
				},
				targetResource: generateTestLabelSelectorTargetResource("1", "aa=bb", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("2", "aa in (bb,cc)", 0),
				},
			},
			want: false,
		},
		{
			name: "test-4",
			args: args{
				priorityAllowedKeyListMap: map[int32]sets.String{
					0: sets.NewString("aa"),
				},
				targetResource: generateTestLabelSelectorTargetResource("1", "aa=bb", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("2", "aa notin (bb,cc)", 0),
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _, err := validateLabelSelectorOverlapped(tt.args.priorityAllowedKeyListMap, tt.args.targetResource, tt.args.otherResources)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLabelSelectorOverlapped() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("validateLabelSelectorOverlapped() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_validateTargetResourceNodeNamesWithOthers(t *testing.T) {
	t.Parallel()

	type args struct {
		targetResource util.KCCTargetResource
		otherResources []util.KCCTargetResource
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "test-1",
			args: args{
				targetResource: generateTestNodeNamesTargetResource("1", []string{"node-1"}),
				otherResources: []util.KCCTargetResource{
					generateTestNodeNamesTargetResource("2", []string{"node-2"}),
				},
			},
			want: true,
		},
		{
			name: "test-2",
			args: args{
				targetResource: generateTestNodeNamesTargetResource("1", []string{"node-1"}),
				otherResources: []util.KCCTargetResource{
					generateTestNodeNamesTargetResource("2", []string{"node-2", "node-3"}),
				},
			},
			want: true,
		},
		{
			name: "test-3",
			args: args{
				targetResource: generateTestNodeNamesTargetResource("1", []string{"node-1"}),
				otherResources: []util.KCCTargetResource{
					generateTestNodeNamesTargetResource("2", []string{"node-1", "node-3"}),
				},
			},
			want: false,
		},
		{
			name: "test-4",
			args: args{
				targetResource: generateTestNodeNamesTargetResource("1", []string{"node-1", "node-2"}),
				otherResources: []util.KCCTargetResource{
					generateTestNodeNamesTargetResource("2", []string{"node-3", "node-4"}),
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _, err := validateTargetResourceNodeNamesOverlapped(tt.args.targetResource, tt.args.otherResources)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTargetResourceNodeNamesOverlapped() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("validateTargetResourceNodeNamesOverlapped() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_validateTargetResourceGlobalWithOthers(t *testing.T) {
	t.Parallel()

	type args struct {
		targetResource util.KCCTargetResource
		otherResources []util.KCCTargetResource
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "test-1",
			args: args{
				targetResource: generateTestLabelSelectorTargetResource("1", "", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("2", "", 0),
				},
			},
			want: false,
		},
		{
			name: "test-2",
			args: args{
				targetResource: generateTestLabelSelectorTargetResource("1", "", 0),
				otherResources: []util.KCCTargetResource{
					generateTestLabelSelectorTargetResource("1", "", 0),
					generateTestLabelSelectorTargetResource("2", "aa=bb", 0),
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, _, err := validateTargetResourceGlobalOverlapped(tt.args.targetResource, tt.args.otherResources)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTargetResourceGlobalOverlapped() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("validateTargetResourceGlobalOverlapped() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func targetResourcesEqual(t1, t2 util.KCCTargetResource) bool {
	status1 := t1.GetGenericStatus()
	status2 := t2.GetGenericStatus()
	if len(status1.Conditions) != len(status2.Conditions) {
		return false
	}

	status1.Conditions[0].LastTransitionTime = metav1.Time{}
	status2.Conditions[0].LastTransitionTime = metav1.Time{}
	t1.SetGenericStatus(status1)
	t2.SetGenericStatus(status2)
	if !apiequality.Semantic.DeepEqual(t1, t2) {
		return false
	}

	return true
}

func Test_updateInvalidTargetResourceStatus(t *testing.T) {
	t.Parallel()

	type args struct {
		targetResource util.KCCTargetResource
		msg            string
		reason         string
	}
	tests := []struct {
		name         string
		args         args
		wantResource util.KCCTargetResource
	}{
		{
			name: "test-1",
			args: args{
				targetResource: util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
					},
				})),
				msg:    "ssasfr",
				reason: "dasf",
			},
			wantResource: util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name: "config-1",
				},
				Spec: v1alpha1.AdminQoSConfigurationSpec{
					GenericConfigSpec: v1alpha1.GenericConfigSpec{
						NodeLabelSelector: "aa=bb",
					},
				},
				Status: v1alpha1.GenericConfigStatus{
					Conditions: []v1alpha1.GenericConfigCondition{
						{
							Type:    v1alpha1.ConfigConditionTypeValid,
							Status:  v1.ConditionFalse,
							Reason:  "dasf",
							Message: "ssasfr",
						},
					},
				},
			})),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if updateInvalidTargetResourceStatus(tt.args.targetResource, tt.args.msg, tt.args.reason); !targetResourcesEqual(tt.args.targetResource, tt.wantResource) {
				t.Errorf("updateInvalidTargetResourceStatus() = %v, want %v", tt.args.targetResource.GetGenericStatus(), tt.wantResource.GetGenericStatus())
			}
		})
	}
}

func Test_updateValidTargetResourceStatus(t *testing.T) {
	t.Parallel()

	type args struct {
		targetResource                                             util.KCCTargetResource
		targetNodes, canaryNodes, updatedTargetNodes, updatedNodes int32
		currentHash                                                string
	}
	tests := []struct {
		name         string
		args         args
		wantResource util.KCCTargetResource
	}{
		{
			name: "test-1",
			args: args{
				targetResource: util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: v1alpha1.AdminQoSConfigurationSpec{
						GenericConfigSpec: v1alpha1.GenericConfigSpec{
							NodeLabelSelector: "aa=bb",
						},
					},
				})),
				targetNodes:        10000,
				canaryNodes:        8000,
				updatedTargetNodes: 5000,
				updatedNodes:       6000,
				currentHash:        "hash-1",
			},
			wantResource: util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name: "config-1",
				},
				Spec: v1alpha1.AdminQoSConfigurationSpec{
					GenericConfigSpec: v1alpha1.GenericConfigSpec{
						NodeLabelSelector: "aa=bb",
					},
				},
				Status: v1alpha1.GenericConfigStatus{
					TargetNodes:        10000,
					CanaryNodes:        8000,
					UpdatedTargetNodes: 5000,
					UpdatedNodes:       6000,
					CurrentHash:        "hash-1",
					Conditions: []v1alpha1.GenericConfigCondition{
						{
							Type:   v1alpha1.ConfigConditionTypeValid,
							Status: v1.ConditionTrue,
							Reason: kccConditionTypeValidReasonNormal,
						},
					},
				},
			})),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if updateValidTargetResourceStatus(
				tt.args.targetResource,
				tt.args.targetNodes,
				tt.args.canaryNodes,
				tt.args.updatedTargetNodes,
				tt.args.updatedNodes,
				tt.args.currentHash,
			); !targetResourcesEqual(tt.args.targetResource, tt.wantResource) {
				t.Errorf("updateValidTargetResourceStatus() = %v, want %v", tt.args.targetResource.GetGenericStatus(), tt.wantResource.GetGenericStatus())
			}
		})
	}
}

func Test_checkLabelSelectorOverlap(t *testing.T) {
	t.Parallel()

	type args struct {
		selector      labels.Selector
		otherSelector labels.Selector
		keyList       []string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "test-1",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1=bb"),
				keyList:       []string{"label1"},
			},
			want: false,
		},
		{
			name: "test-2",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1!=bb"),
				keyList:       []string{"label1"},
			},
			want: true,
		},
		{
			name: "test-3",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1 in (aa,bb)"),
				keyList:       []string{"label1"},
			},
			want: true,
		},
		{
			name: "test-4",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1 notin (aa,bb)"),
				keyList:       []string{"label1"},
			},
			want: false,
		},
		{
			name: "test-5",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1 in (aa,bb),label2=cc"),
				keyList:       []string{"label1", "label2"},
			},
			want: true,
		},
		{
			name: "test-6",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label2=bb"),
				keyList:       []string{"label1", "label2"},
			},
			want: true,
		},
		{
			name: "test-7",
			args: args{
				selector:      testLabelSelector(t, "label1 notin (aa, bb),label2=bb"),
				otherSelector: testLabelSelector(t, "label1 in (aa),label2=bb"),
				keyList:       []string{"label1", "label2"},
			},
			want: false,
		},
		{
			name: "test-8",
			args: args{
				selector:      testLabelSelector(t, "label1 in (aa),label2 notin (bb,cc)"),
				otherSelector: testLabelSelector(t, "label1 notin (cc,dd),label2 notin (cc)"),
				keyList:       []string{"label1", "label2"},
			},
			want: true,
		},
		{
			name: "test-9",
			args: args{
				selector:      testLabelSelector(t, "label1=aa"),
				otherSelector: testLabelSelector(t, "label1 notin (cc,dd),label2 notin (cc)"),
				keyList:       []string{"label1", "label2"},
			},
			want: true,
		},
		{
			name: "test-10",
			args: args{
				selector:      testLabelSelector(t, "label1 notin (aa)"),
				otherSelector: testLabelSelector(t, "label1=cc"),
				keyList:       []string{"label1", "label2"},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, checkLabelSelectorOverlap(tt.args.selector, tt.args.otherSelector, tt.args.keyList), "checkLabelSelectorOverlap(%v, %v, %v)", tt.args.selector, tt.args.otherSelector, tt.args.keyList)
		})
	}
}
