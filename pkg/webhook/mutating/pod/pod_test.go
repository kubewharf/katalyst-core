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

package pod

import (
	"context"
	"encoding/json"
	"testing"

	whcontext "github.com/slok/kubewebhook/pkg/webhook/context"
	"github.com/stretchr/testify/assert"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	webhookconfig "github.com/kubewharf/katalyst-core/pkg/config/webhook"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var stsGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}

func getPodJSON(pod *v1.Pod) []byte {
	bs, _ := json.Marshal(pod)
	return bs
}

func TestMutatePod(t *testing.T) {
	t.Parallel()

	container1 := &v1.Container{
		Name: "c1",
		Resources: v1.ResourceRequirements{
			Limits: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("4Gi"),
				v1.ResourceCPU:    resource.MustParse("4"),
			},
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceMemory: resource.MustParse("2Gi"),
				v1.ResourceCPU:    resource.MustParse("2"),
			},
		},
	}
	sts1 := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       stsGVK.Kind,
			APIVersion: stsGVK.GroupVersion().String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "sts1",
			Annotations: map[string]string{
				apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: pointer.Int32(1),
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{*container1},
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"workload": "sts",
				},
			},
		},
		Status: appsv1.StatefulSetStatus{},
	}

	vpa1 := &apis.KatalystVerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "vpa1",
		},
		Spec: apis.KatalystVerticalPodAutoscalerSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
			UpdatePolicy: apis.PodUpdatePolicy{
				PodUpdatingStrategy: "",
				PodMatchingStrategy: apis.PodMatchingStrategyAll,
				PodApplyStrategy:    "",
			},
		},
		Status: apis.KatalystVerticalPodAutoscalerStatus{
			ContainerResources: []apis.ContainerResources{
				{
					ContainerName: pointer.String("c1"),
					Requests: &apis.ContainerResourceList{
						Target: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
						UncappedTarget: map[v1.ResourceName]resource.Quantity{
							v1.ResourceMemory: resource.MustParse("1Gi"),
							v1.ResourceCPU:    resource.MustParse("1"),
						},
					},
				},
			},
		},
	}

	spd1 := &workloadapis.ServiceProfileDescriptor{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "spd1",
		},
		Spec: workloadapis.ServiceProfileDescriptorSpec{
			TargetRef: apis.CrossVersionObjectReference{
				Kind:       stsGVK.Kind,
				Name:       "sts1",
				APIVersion: stsGVK.GroupVersion().String(),
			},
		},
		Status: workloadapis.ServiceProfileDescriptorStatus{},
	}

	pod1 := &v1.Pod{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod1",
			Annotations: map[string]string{
				apiconsts.WorkloadAnnotationVPAEnabledKey: apiconsts.WorkloadAnnotationVPAEnabled,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       "sts1",
					Kind:       stsGVK.Kind,
					APIVersion: stsGVK.GroupVersion().String(),
				},
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				*container1,
			},
		},
		Status: v1.PodStatus{},
	}
	pod2 := &v1.Pod{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod2",
			Annotations: map[string]string{
				"k1": "v1",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       "sts1",
					Kind:       stsGVK.Kind,
					APIVersion: stsGVK.GroupVersion().String(),
				},
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				*container1,
			},
		},
		Status: v1.PodStatus{},
	}

	for _, tc := range []struct {
		name     string
		object   runtime.Object
		gvr      string
		vpa      *apis.KatalystVerticalPodAutoscaler
		spd      *workloadapis.ServiceProfileDescriptor
		pod      *v1.Pod
		review   *admissionv1beta1.AdmissionReview
		expPatch []string
	}{
		{
			name:   "mutate pod resource and annotation",
			object: sts1,
			gvr:    "statefulsets.v1.apps",
			vpa:    vpa1,
			spd:    spd1,
			pod:    pod1,
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "test",
					Object: runtime.RawExtension{
						Raw: getPodJSON(pod1),
					},
				},
			},
			expPatch: []string{
				`{"op":"replace","path":"/spec/containers/0/resources/requests/cpu","value":"1"}`,
				`{"op":"replace","path":"/spec/containers/0/resources/requests/memory","value":"1Gi"}`,
			},
		},
		{
			name:   "mutate pod(vpa not enabled) resource and annotation",
			object: sts1,
			gvr:    "statefulsets.v1.apps",
			vpa:    vpa1,
			spd:    spd1,
			pod:    pod2,
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "test",
					Object: runtime.RawExtension{
						Raw: getPodJSON(pod2),
					},
				},
			},
			expPatch: []string{
				`{"op":"replace","path":"/spec/containers/0/resources/requests/cpu","value":"1"}`,
				`{"op":"replace","path":"/spec/containers/0/resources/requests/memory","value":"1Gi"}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			genericConf := &generic.GenericConfiguration{}
			webhookGenericConf := webhookconfig.NewGenericWebhookConfiguration()
			webhookGenericConf.DynamicGVResources = []string{
				"statefulsets.v1.apps",
				"replicasets.v1.apps",
				"deployments.v1.apps",
			}

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, []runtime.Object{tc.object})
			assert.NoError(t, err)

			workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()

			u, err := native.ToUnstructured(tc.object)
			assert.NoError(t, err)
			err = workloadInformers[tc.gvr].Informer.Informer().GetStore().Add(u)
			assert.NoError(t, err)

			wh, _, err := NewWebhookPod(context.TODO(), controlCtx, genericConf, webhookGenericConf, nil)
			assert.NoError(t, err)

			vpaInformer := controlCtx.InternalInformerFactory.Autoscaling().V1alpha1().KatalystVerticalPodAutoscalers()
			err = vpaInformer.Informer().GetStore().Add(tc.vpa)
			assert.NoError(t, err)

			spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors()
			err = spdInformer.Informer().GetStore().Add(tc.spd)
			assert.NoError(t, err)

			podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
			err = podInformer.Informer().GetStore().Add(tc.pod)
			assert.NoError(t, err)

			ar := &admissionv1beta1.AdmissionRequest{
				Name:      tc.pod.Name,
				Namespace: "default",
			}

			ctx := context.TODO()
			ctx = whcontext.SetAdmissionRequest(ctx, ar)

			gotResponse := wh.Review(ctx, tc.review)

			// Check uid, allowed and patch
			assert.True(t, gotResponse.Allowed)
			assert.Equal(t, tc.review.Request.UID, gotResponse.UID)
			gotPatch := string(gotResponse.Patch)
			for _, expPatchOp := range tc.expPatch {
				assert.Contains(t, gotPatch, expPatchOp)
			}
		})
	}
}
