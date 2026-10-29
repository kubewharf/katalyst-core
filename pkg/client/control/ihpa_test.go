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

package control

import (
	"context"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha2"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateHPA(t *testing.T) {
	t.Parallel()

	new1 := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		hpaName string
		new     *autoscalingv2.HorizontalPodAutoscaler
	}{
		{
			name:    "hpa not found",
			hpaName: new1.Name,
		},
		{
			name: "normal",
			new:  new1,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := fake.NewSimpleClientset()
			manager, err := NewHPAManager(internalClient, newHPADiscovery("autoscaling/v2"))
			require.NoError(t, err)

			if tc.new == nil {
				hpa, err := manager.Create(context.TODO(), tc.new, metav1.CreateOptions{})
				assert.Nil(t, hpa)
				assert.Error(t, err)
				_, err = internalClient.AutoscalingV2().HorizontalPodAutoscalers("default").
					Get(context.TODO(), tc.hpaName, metav1.GetOptions{})
				assert.Error(t, err)
			} else {
				hpa, err := manager.Create(context.TODO(), tc.new, metav1.CreateOptions{})
				assert.NoError(t, err)
				hpa2, err := internalClient.AutoscalingV2().HorizontalPodAutoscalers("default").
					Get(context.TODO(), tc.new.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, hpa, hpa2)
			}
		})
	}
}

func TestPatchHPA(t *testing.T) {
	t.Parallel()

	old1 := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hpa1",
			Namespace: "default",
		},
	}
	new1 := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		old     *autoscalingv2.HorizontalPodAutoscaler
		new     *autoscalingv2.HorizontalPodAutoscaler
		wantErr bool
	}{
		{
			name: "add annotation",
			old:  old1,
			new:  new1,
		},
		{
			name: "delete annotation",
			old:  new1,
			new:  old1,
		},
		{
			name:    "nil",
			old:     old1,
			new:     nil,
			wantErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := fake.NewSimpleClientset(tc.old)
			updater, err := NewHPAManager(internalClient, newHPADiscovery("autoscaling/v2"))
			require.NoError(t, err)
			_, err = updater.Patch(context.TODO(), tc.old, tc.new, metav1.PatchOptions{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				hpa, err := internalClient.AutoscalingV2().HorizontalPodAutoscalers("default").
					Get(context.TODO(), tc.old.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, tc.new, hpa)
			}
		})
	}
}

func TestNewHPAManagerPrefersV2(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	manager, err := NewHPAManager(client, newHPADiscovery("autoscaling/v2", "autoscaling/v2beta2"))
	require.NoError(t, err)

	expected := testHPA()
	actual, err := manager.Create(context.Background(), expected, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, expected, actual)

	_, err = client.AutoscalingV2().HorizontalPodAutoscalers(expected.Namespace).
		Get(context.Background(), expected.Name, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = client.AutoscalingV2beta2().HorizontalPodAutoscalers(expected.Namespace).
		Get(context.Background(), expected.Name, metav1.GetOptions{})
	assert.Error(t, err)
}

func TestNewHPAManagerFallsBackToV2beta2(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	manager, err := NewHPAManager(client, newHPADiscovery("autoscaling/v2beta2"))
	require.NoError(t, err)

	expected := testHPA()
	actual, err := manager.Create(context.Background(), expected, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.Equal(t, expected, actual)

	stored, err := client.AutoscalingV2beta2().HorizontalPodAutoscalers(expected.Namespace).
		Get(context.Background(), expected.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expected.Spec.ScaleTargetRef.Name, stored.Spec.ScaleTargetRef.Name)

	fetched, err := manager.Get(
		context.Background(), expected.Namespace, expected.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, expected, fetched)

	updated := fetched.DeepCopy()
	updated.Spec.MaxReplicas++
	patched, err := manager.Patch(
		context.Background(), fetched, updated, metav1.PatchOptions{})
	require.NoError(t, err)
	assert.Equal(t, updated, patched)
}

func TestNewHPAManagerRejectsUnsupportedAPI(t *testing.T) {
	t.Parallel()

	manager, err := NewHPAManager(
		fake.NewSimpleClientset(),
		&fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}},
	)
	assert.Nil(t, manager)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover HorizontalPodAutoscaler API")
}

func TestConvertHPAV2beta2RoundTrip(t *testing.T) {
	t.Parallel()

	expected := testHPA()
	external := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	require.NoError(t, convertHPA(expected, external))

	actual := &autoscalingv2.HorizontalPodAutoscaler{}
	require.NoError(t, convertHPA(external, actual))
	assert.Equal(t, expected, actual)
}

func newHPADiscovery(groupVersions ...string) *fakediscovery.FakeDiscovery {
	resources := make([]*metav1.APIResourceList, 0, len(groupVersions))
	for _, groupVersion := range groupVersions {
		resources = append(resources, &metav1.APIResourceList{
			GroupVersion: groupVersion,
			APIResources: []metav1.APIResource{{
				Name:  "horizontalpodautoscalers",
				Verbs: metav1.Verbs{"get", "create", "patch"},
			}},
		})
	}
	return &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: resources}}
}

func testHPA() *autoscalingv2.HorizontalPodAutoscaler {
	minReplicas := int32(2)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-hpa",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "autoscaling.katalyst.kubewharf.io/v1alpha2",
				Kind:       "IntelligentHorizontalPodAutoscaler",
				Name:       "test-ihpa",
			}},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "test-workload",
			},
			MinReplicas: &minReplicas,
			MaxReplicas: 10,
			Metrics: []autoscalingv2.MetricSpec{
				{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: "cpu",
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: func() *int32 { value := int32(60); return &value }(),
						},
					},
				},
				{
					Type: autoscalingv2.ExternalMetricSourceType,
					External: &autoscalingv2.ExternalMetricSource{
						Metric: autoscalingv2.MetricIdentifier{Name: "queue_depth"},
						Target: autoscalingv2.MetricTarget{
							Type:  autoscalingv2.ValueMetricType,
							Value: resourceQuantity("5"),
						},
					},
				},
			},
			Behavior: &autoscalingv2.HorizontalPodAutoscalerBehavior{
				ScaleUp: &autoscalingv2.HPAScalingRules{
					SelectPolicy: func() *autoscalingv2.ScalingPolicySelect {
						value := autoscalingv2.MaxChangePolicySelect
						return &value
					}(),
					Policies: []autoscalingv2.HPAScalingPolicy{{
						Type:          autoscalingv2.PodsScalingPolicy,
						Value:         2,
						PeriodSeconds: 60,
					}},
				},
			},
		},
	}
}

func resourceQuantity(value string) *resource.Quantity {
	quantity := resource.MustParse(value)
	return &quantity
}

func TestUpdateIHPA(t *testing.T) {
	t.Parallel()

	oldihpa1 := &v1alpha2.IntelligentHorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
	}
	newihpa1 := &v1alpha2.IntelligentHorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		old     *v1alpha2.IntelligentHorizontalPodAutoscaler
		new     *v1alpha2.IntelligentHorizontalPodAutoscaler
		wantErr bool
	}{
		{
			name: "add annotation",
			old:  oldihpa1,
			new:  newihpa1,
		},
		{
			name: "delete annotation",
			old:  newihpa1,
			new:  oldihpa1,
		},
		{
			name:    "nil",
			old:     oldihpa1,
			new:     nil,
			wantErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.old)
			updater := NewIHPAUpdater(internalClient)
			_, err := updater.Update(context.TODO(), tc.new, metav1.UpdateOptions{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				ihpa, err := internalClient.AutoscalingV1alpha2().IntelligentHorizontalPodAutoscalers("default").
					Get(context.TODO(), tc.old.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, tc.new, ihpa)
			}
		})
	}
}

func TestUpdateIHPAStatus(t *testing.T) {
	t.Parallel()

	oldihpa1 := &v1alpha2.IntelligentHorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
	}
	newihpa1 := &v1alpha2.IntelligentHorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
		Status: v1alpha2.IntelligentHorizontalPodAutoscalerStatus{
			LastScaleTime: &metav1.Time{Time: time.Now()},
		},
	}

	for _, tc := range []struct {
		name    string
		old     *v1alpha2.IntelligentHorizontalPodAutoscaler
		new     *v1alpha2.IntelligentHorizontalPodAutoscaler
		wantErr bool
	}{
		{
			name: "add annotation",
			old:  oldihpa1,
			new:  newihpa1,
		},
		{
			name: "delete annotation",
			old:  newihpa1,
			new:  oldihpa1,
		},
		{
			name:    "nil",
			old:     oldihpa1,
			new:     nil,
			wantErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.old)
			updater := NewIHPAUpdater(internalClient)
			_, err := updater.UpdateStatus(context.TODO(), tc.new, metav1.UpdateOptions{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				ihpa, err := internalClient.AutoscalingV1alpha2().IntelligentHorizontalPodAutoscalers("default").
					Get(context.TODO(), tc.old.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, tc.new, ihpa)
			}
		})
	}
}

func TestCreateVW(t *testing.T) {
	t.Parallel()

	new1 := &v1alpha2.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vw1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		hpaName string
		new     *v1alpha2.VirtualWorkload
	}{
		{
			name:    "vw not found",
			hpaName: new1.Name,
		},
		{
			name: "normal",
			new:  new1,
		},
		{
			name: "nil",
			new:  nil,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset()
			manager := NewVirtualWorkloadManager(internalClient)

			if tc.new == nil {
				_, err := internalClient.AutoscalingV1alpha2().VirtualWorkloads("default").
					Get(context.TODO(), tc.hpaName, metav1.GetOptions{})
				assert.Error(t, err)
			} else {
				hpa, err := manager.Create(context.TODO(), tc.new, metav1.CreateOptions{})
				assert.NoError(t, err)
				hpa2, err := internalClient.AutoscalingV1alpha2().VirtualWorkloads("default").
					Get(context.TODO(), tc.new.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, hpa, hpa2)
			}
		})
	}
}

func TestUpdateVW(t *testing.T) {
	t.Parallel()

	oldivw1 := &v1alpha2.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
	}
	newivw1 := &v1alpha2.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
			Annotations: map[string]string{
				"a1": "a1",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		old     *v1alpha2.VirtualWorkload
		new     *v1alpha2.VirtualWorkload
		wantErr bool
	}{
		{
			name: "add annotation",
			old:  oldivw1,
			new:  newivw1,
		},
		{
			name: "delete annotation",
			old:  newivw1,
			new:  oldivw1,
		},
		{
			name:    "nil",
			old:     oldivw1,
			new:     nil,
			wantErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.old)
			updater := NewVirtualWorkloadManager(internalClient)
			_, err := updater.Update(context.TODO(), tc.new, metav1.UpdateOptions{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				ihpa, err := internalClient.AutoscalingV1alpha2().VirtualWorkloads("default").
					Get(context.TODO(), tc.old.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, tc.new, ihpa)
			}
		})
	}
}

func TestUpdateVWStatus(t *testing.T) {
	t.Parallel()

	oldivw1 := &v1alpha2.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
	}
	newivw1 := &v1alpha2.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ihpa1",
			Namespace: "default",
		},
		Status: v1alpha2.VirtualWorkloadStatus{
			Replicas: 1,
		},
	}

	for _, tc := range []struct {
		name    string
		old     *v1alpha2.VirtualWorkload
		new     *v1alpha2.VirtualWorkload
		wantErr bool
	}{
		{
			name: "add replicas",
			old:  oldivw1,
			new:  newivw1,
		},
		{
			name:    "nil",
			old:     oldivw1,
			new:     nil,
			wantErr: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			internalClient := externalfake.NewSimpleClientset(tc.old)
			updater := NewVirtualWorkloadManager(internalClient)
			_, err := updater.UpdateStatus(context.TODO(), tc.new, metav1.UpdateOptions{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				ihpa, err := internalClient.AutoscalingV1alpha2().VirtualWorkloads("default").
					Get(context.TODO(), tc.old.Name, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, tc.new, ihpa)
			}
		})
	}
}
