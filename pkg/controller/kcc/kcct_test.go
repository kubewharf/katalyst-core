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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	kccutil "github.com/kubewharf/katalyst-core/pkg/controller/kcc/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

func Test_getCNCUpdateScope(t *testing.T) {
	t.Parallel()

	newCNC := func() *v1alpha1.CustomNodeConfig {
		return &v1alpha1.CustomNodeConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "node-1",
				Labels:          map[string]string{"zone": "a"},
				ResourceVersion: "1",
			},
			Status: v1alpha1.CustomNodeConfigStatus{
				KatalystCustomConfigList: []v1alpha1.TargetConfig{
					{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "old"},
					{ConfigType: crd.AuthConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "same"},
				},
			},
		}
	}

	tests := []struct {
		name     string
		mutate   func(*v1alpha1.CustomNodeConfig)
		wantAll  bool
		wantGVRs []metav1.GroupVersionResource
	}{
		{
			name: "one config hash changed",
			mutate: func(cnc *v1alpha1.CustomNodeConfig) {
				cnc.Status.KatalystCustomConfigList[0].Hash = "new"
			},
			wantGVRs: []metav1.GroupVersionResource{crd.AdminQoSConfigurationGVR},
		},
		{
			name: "config list reordered",
			mutate: func(cnc *v1alpha1.CustomNodeConfig) {
				cnc.Status.KatalystCustomConfigList[0], cnc.Status.KatalystCustomConfigList[1] =
					cnc.Status.KatalystCustomConfigList[1], cnc.Status.KatalystCustomConfigList[0]
			},
		},
		{
			name: "unrelated status changed",
			mutate: func(cnc *v1alpha1.CustomNodeConfig) {
				cnc.Status.ServiceProfileConfigList = []v1alpha1.TargetConfig{
					{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "profile", Hash: "new"},
				}
			},
		},
		{
			name: "unregistered config changed",
			mutate: func(cnc *v1alpha1.CustomNodeConfig) {
				cnc.Status.KatalystCustomConfigList = append(cnc.Status.KatalystCustomConfigList, v1alpha1.TargetConfig{
					ConfigType: metav1.GroupVersionResource{Group: "test.katalyst.kubewharf.io", Version: "v1alpha1", Resource: "testconfigurations"},
					Hash:       "new",
				})
			},
		},
		{
			name: "labels changed",
			mutate: func(cnc *v1alpha1.CustomNodeConfig) {
				cnc.Labels["zone"] = "b"
			},
			wantAll: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oldCNC := newCNC()
			newCNC := oldCNC.DeepCopy()
			newCNC.ResourceVersion = "2"
			tt.mutate(newCNC)

			gotAll, gotGVRs := getCNCUpdateScope(oldCNC, newCNC, func(gvr metav1.GroupVersionResource) bool {
				return gvr == crd.AdminQoSConfigurationGVR || gvr == crd.AuthConfigurationGVR
			})
			assert.Equal(t, tt.wantAll, gotAll)
			assert.ElementsMatch(t, tt.wantGVRs, gotGVRs)
		})
	}
}

func Test_calculateTargetStatusCounts(t *testing.T) {
	t.Parallel()

	target := util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "global"},
	}))
	targetKey := native.GenerateUniqObjectNameKey(target)
	cncs := []*v1alpha1.CustomNodeConfig{
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "current"}}}},
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "current"}}}},
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "old"}}}},
	}
	counts := calculateTargetStatusCounts(
		crd.AdminQoSConfigurationGVR,
		cncs,
		[]util.KCCTargetResource{target},
		map[string]string{targetKey: "current"},
		map[string]int{targetKey: 1},
		map[string][]int{targetKey: {0, 2}},
		[]cncAppliedState{
			{namespace: "default", name: "global", hash: "current", found: true},
			{namespace: "default", name: "global", hash: "current", found: true},
			{namespace: "default", name: "global", hash: "old", found: true},
		},
	)

	assert.Equal(t, targetStatusCounts{
		targetNodes:        2,
		canaryNodes:        1,
		updatedTargetNodes: 1,
		updatedNodes:       2,
	}, counts[targetKey])
}

func Test_calculateTargetStatusCountsMatchesLegacy(t *testing.T) {
	t.Parallel()

	targets := []util.KCCTargetResource{
		util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "target-a"},
		})),
		util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "target-b"},
		})),
	}
	targetAKey := native.GenerateUniqObjectNameKey(targets[0])
	targetBKey := native.GenerateUniqObjectNameKey(targets[1])
	hashes := map[string]string{targetAKey: "hash-a", targetBKey: "hash-b"}
	canaryCutoffPoints := map[string]int{targetAKey: 2, targetBKey: 2}
	targetCNCIndexes := map[string][]int{targetAKey: {0, 4}, targetBKey: {1, 2, 3}}
	cncs := []*v1alpha1.CustomNodeConfig{
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "target-a", Hash: "hash-a"},
		}}},
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "target-a", Hash: "hash-a"},
		}}},
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "target-b", Hash: "hash-b"},
		}}},
		{Status: v1alpha1.CustomNodeConfigStatus{KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "target-b", Hash: "old"},
		}}},
		{},
	}
	appliedStates := make([]cncAppliedState, len(cncs))
	for i, cnc := range cncs {
		appliedStates[i] = getCNCAppliedState(cnc, crd.AdminQoSConfigurationGVR)
	}

	got := calculateTargetStatusCounts(crd.AdminQoSConfigurationGVR, cncs, targets, hashes, canaryCutoffPoints, targetCNCIndexes, appliedStates)
	for _, target := range targets {
		targetKey := native.GenerateUniqObjectNameKey(target)
		targetIndexSet := sets.NewInt(targetCNCIndexes[targetKey]...)
		want := targetStatusCounts{
			targetNodes: int32(len(targetCNCIndexes[targetKey])),
			canaryNodes: int32(canaryCutoffPoints[targetKey]),
		}
		for i, cnc := range cncs {
			if kccutil.IsCNCUpdated(cnc, crd.AdminQoSConfigurationGVR, target, hashes[targetKey]) {
				want.updatedNodes++
				if targetIndexSet.Has(i) {
					want.updatedTargetNodes++
				}
			}
		}
		assert.Equal(t, want, got[targetKey])
	}
}

func Test_getCNCAppliedState(t *testing.T) {
	t.Parallel()

	cnc := &v1alpha1.CustomNodeConfig{Status: v1alpha1.CustomNodeConfigStatus{
		KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AuthConfigurationGVR, ConfigNamespace: "default", ConfigName: "auth", Hash: "auth-hash"},
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "aqc-hash"},
		},
	}}
	assert.Equal(t, cncAppliedState{
		namespace: "default",
		name:      "global",
		hash:      "aqc-hash",
		found:     true,
	}, getCNCAppliedState(cnc, crd.AdminQoSConfigurationGVR))
	assert.Equal(t, cncAppliedState{}, getCNCAppliedState(cnc, crd.IRQTuningConfigurationGVR))
}

func Test_getCNCAppliedStateUsesAnyMatchingDuplicate(t *testing.T) {
	t.Parallel()

	cnc := &v1alpha1.CustomNodeConfig{Status: v1alpha1.CustomNodeConfigStatus{
		KatalystCustomConfigList: []v1alpha1.TargetConfig{
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "old", Hash: "old"},
			{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "current"},
		},
	}}
	target := util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "global"},
	}))
	targetKey := native.GenerateUniqObjectNameKey(target)
	state := getCNCAppliedState(cnc, crd.AdminQoSConfigurationGVR)
	counts := calculateTargetStatusCounts(
		crd.AdminQoSConfigurationGVR,
		[]*v1alpha1.CustomNodeConfig{cnc},
		[]util.KCCTargetResource{target},
		map[string]string{targetKey: "current"},
		map[string]int{targetKey: 1},
		map[string][]int{targetKey: {0}},
		[]cncAppliedState{state},
	)

	assert.True(t, kccutil.IsCNCUpdated(cnc, crd.AdminQoSConfigurationGVR, target, "current"))
	assert.Equal(t, int32(1), counts[targetKey].updatedNodes)
	assert.Equal(t, int32(1), counts[targetKey].updatedTargetNodes)
}

func benchmarkTargetStatusData() ([]*v1alpha1.CustomNodeConfig, util.KCCTargetResource, map[string]string, map[string][]int) {
	const cncCount = 13550
	target := util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "global"},
	}))
	targetKey := native.GenerateUniqObjectNameKey(target)
	cncs := make([]*v1alpha1.CustomNodeConfig, cncCount)
	targetIndexes := make([]int, cncCount)
	for i := range cncs {
		cncs[i] = &v1alpha1.CustomNodeConfig{Status: v1alpha1.CustomNodeConfigStatus{
			KatalystCustomConfigList: []v1alpha1.TargetConfig{
				{ConfigType: crd.AuthConfigurationGVR, ConfigNamespace: "default", ConfigName: "auth", Hash: "hash"},
				{ConfigType: crd.AdminQoSConfigurationGVR, ConfigNamespace: "default", ConfigName: "global", Hash: "hash"},
				{ConfigType: crd.TransparentMemoryOffloadingConfigurationGVR, ConfigNamespace: "default", ConfigName: "tmo", Hash: "hash"},
				{ConfigType: crd.StrategyGroupGVR, ConfigNamespace: "default", ConfigName: "strategy", Hash: "hash"},
				{ConfigType: crd.IRQTuningConfigurationGVR, ConfigNamespace: "default", ConfigName: "irq", Hash: "hash"},
				{ConfigType: metav1.GroupVersionResource{Group: "test.katalyst.kubewharf.io", Version: "v1alpha1", Resource: "testconfigurations"}, ConfigNamespace: "default", ConfigName: "test", Hash: "hash"},
			},
		}}
		targetIndexes[i] = i
	}
	return cncs, target, map[string]string{targetKey: "hash"}, map[string][]int{targetKey: targetIndexes}
}

func BenchmarkTargetStatusCalculation(b *testing.B) {
	cncs, target, hashes, targetCNCIndexes := benchmarkTargetStatusData()
	targetKey := native.GenerateUniqObjectNameKey(target)
	targetResources := []util.KCCTargetResource{target}
	canaryCutoffPoints := map[string]int{targetKey: len(cncs)}

	b.Run("repeated-list-scan", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, cnc := range cncs {
				_ = kccutil.IsCNCUpdated(cnc, crd.AdminQoSConfigurationGVR, target, hashes[targetKey])
			}
			for _, cnc := range cncs {
				_ = kccutil.IsCNCUpdated(cnc, crd.AdminQoSConfigurationGVR, target, hashes[targetKey])
			}
		}
	})

	b.Run("single-applied-state-scan", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			appliedStates := make([]cncAppliedState, len(cncs))
			for i, cnc := range cncs {
				appliedStates[i] = getCNCAppliedState(cnc, crd.AdminQoSConfigurationGVR)
			}
			_ = calculateTargetStatusCounts(crd.AdminQoSConfigurationGVR, cncs, targetResources, hashes, canaryCutoffPoints, targetCNCIndexes, appliedStates)
		}
	})
}

type failingCNCControl struct {
	control.DummyCNCControl
}

func (f failingCNCControl) PatchCNCStatus(_ context.Context, _ string, _, _ *v1alpha1.CustomNodeConfig) (*v1alpha1.CustomNodeConfig, error) {
	return nil, errors.New("patch failed")
}

func Test_updateCNCsPreservesAppliedStateOnPatchFailure(t *testing.T) {
	t.Parallel()

	target := util.ToKCCTargetResource(toTestUnstructured(&v1alpha1.AdminQoSConfiguration{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "global"},
	}))
	targetKey := native.GenerateUniqObjectNameKey(target)
	cnc := &v1alpha1.CustomNodeConfig{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	appliedStates := []cncAppliedState{{namespace: "default", name: "global", hash: "old", found: true}}
	controller := &KatalystCustomConfigTargetController{
		ctx:            context.Background(),
		cncControl:     failingCNCControl{},
		metricsEmitter: metrics.DummyMetrics{},
		cncUpdateQPS:   defaultCNCUpdateQPS,
		cncUpdateBurst: defaultCNCUpdateBurst,
	}

	rateLimited, gotErrors := controller.updateCNCs(
		crd.AdminQoSConfigurationGVR,
		[]util.KCCTargetResource{target},
		map[string]string{targetKey: "new"},
		map[string]int{targetKey: 1},
		map[string][]int{targetKey: {0}},
		[]*v1alpha1.CustomNodeConfig{cnc},
		appliedStates,
	)

	assert.False(t, rateLimited)
	assert.Len(t, gotErrors, 1)
	assert.Equal(t, "old", appliedStates[0].hash)
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
