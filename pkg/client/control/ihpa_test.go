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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
			manager := NewHPAManager(internalClient)

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
			updater := NewHPAManager(internalClient)
			_, err := updater.Patch(context.TODO(), tc.old, tc.new, metav1.PatchOptions{})
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
