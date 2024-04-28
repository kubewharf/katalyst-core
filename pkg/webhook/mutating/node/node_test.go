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

package node

import (
	"context"
	"encoding/json"
	"testing"

	whcontext "github.com/slok/kubewebhook/pkg/webhook/context"
	"github.com/stretchr/testify/assert"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
)

func TestMutateNode(t *testing.T) {
	t.Parallel()

	node0 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node0",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationCPUOvercommitRatioKey:    "2",
				apiconsts.NodeAnnotationMemoryOvercommitRatioKey: "1.2",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node1",
			Namespace: "default",
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node2",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationCPUOvercommitRatioKey: "2",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node3 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node3",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationMemoryOvercommitRatioKey: "1.2",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node4 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node4",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationMemoryOvercommitRatioKey: "1.2",
				apiconsts.NodeAnnotationCPUOvercommitRatioKey:    "illegal value",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node5 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node5",
			Namespace: "default",
			Annotations: map[string]string{
				"testKey": "testVal",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node6 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node6",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationCPUOvercommitRatioKey:            "2",
				apiconsts.NodeAnnotationMemoryOvercommitRatioKey:         "1.2",
				apiconsts.NodeAnnotationRealtimeCPUOvercommitRatioKey:    "1",
				apiconsts.NodeAnnotationRealtimeMemoryOvercommitRatioKey: "1",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}
	node7 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node6",
			Namespace: "default",
			Annotations: map[string]string{
				apiconsts.NodeAnnotationCPUOvercommitRatioKey:            "2",
				apiconsts.NodeAnnotationMemoryOvercommitRatioKey:         "1.2",
				apiconsts.NodeAnnotationRealtimeCPUOvercommitRatioKey:    "2",
				apiconsts.NodeAnnotationRealtimeMemoryOvercommitRatioKey: "2",
				apiconsts.NodeAnnotationOvercommitAllocatableCPUKey:      "80",
				apiconsts.NodeAnnotationOvercommitCapacityCPUKey:         "80",
				apiconsts.NodeAnnotationOvercommitAllocatableMemoryKey:   "372Gi",
				apiconsts.NodeAnnotationOvercommitCapacityMemoryKey:      "384Gi",
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(48, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("192Gi"),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewQuantity(44, resource.DecimalSI),
				v1.ResourceMemory: resource.MustParse("186Gi"),
			},
		},
	}

	cases := []struct {
		name     string
		review   *admissionv1beta1.AdmissionReview
		expPatch []string
		allow    bool
	}{
		{
			name: "node with overcommit annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case0",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node0),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			// 44 * 2 = 88, 186 * 1024 * 1024 * 1024 * 1.2 = 239659175116.8
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
				`{"op":"replace","path":"/status/allocatable/cpu","value":"88"},{"op":"replace","path":"/status/allocatable/memory","value":"239659175116"}`,
				`{"op":"replace","path":"/status/capacity/cpu","value":"96"},{"op":"replace","path":"/status/capacity/memory","value":"247390116249"}`,
			},
			allow: true,
		},
		{
			name: "node without annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case1",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node1),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations","value":{"katalyst.kubewharf.io/original_allocatable_cpu":"44","katalyst.kubewharf.io/original_allocatable_memory":"186Gi","katalyst.kubewharf.io/original_capacity_cpu":"48","katalyst.kubewharf.io/original_capacity_memory":"192Gi"}}`,
			},
			allow: true,
		},
		{
			name: "node with only CPU overcommit annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case2",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node2),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
				`{"op":"replace","path":"/status/allocatable/cpu","value":"88"}`,
				`{"op":"replace","path":"/status/capacity/cpu","value":"96"}`,
			},
			allow: true,
		},
		{
			name: "node with only memory overcommit annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case3",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node3),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
				`{"op":"replace","path":"/status/allocatable/memory","value":"239659175116"}`,
				`{"op":"replace","path":"/status/capacity/memory","value":"247390116249"}`,
			},
			allow: true,
		},
		{
			name: "node with illegal overcommit annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case4",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node4),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
				`{"op":"replace","path":"/status/allocatable/memory","value":"239659175116"}`,
				`{"op":"replace","path":"/status/capacity/memory","value":"247390116249"}`,
			},
			allow: true,
		},
		{
			name: "CREATE request",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case5",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node0),
					},
					Operation:   admissionv1beta1.Create,
					SubResource: "status",
				},
			},
			expPatch: []string{`[]`},
			allow:    true,
		},
		{
			name: "node without overcommit annotation",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case6",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node5),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
			},
			allow: true,
		},
		{
			name: "node with lower recommend",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case7",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node6),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
			},
			allow: true,
		},
		{
			name: "node with allocatable",
			review: &admissionv1beta1.AdmissionReview{
				Request: &admissionv1beta1.AdmissionRequest{
					UID: "case8",
					Object: runtime.RawExtension{
						Raw: nodeToJson(node7),
					},
					Operation:   admissionv1beta1.Update,
					SubResource: "status",
				},
			},
			expPatch: []string{
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_cpu","value":"44"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_allocatable_memory","value":"186Gi"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_cpu","value":"48"}`,
				`{"op":"add","path":"/metadata/annotations/katalyst.kubewharf.io~1original_capacity_memory","value":"192Gi"}`,
				`{"op":"replace","path":"/status/allocatable/cpu","value":"80"}`,
				`{"op":"replace","path":"/status/capacity/cpu","value":"80"}`,
				`{"op":"replace","path":"/status/allocatable/memory","value":"372Gi"}`,
				`{"op":"replace","path":"/status/capacity/memory","value":"384Gi"}`,
			},
			allow: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			controlCtx, err := katalystbase.GenerateFakeGenericContext()
			assert.NoError(t, err)

			ctx := context.Background()
			ctx = whcontext.SetAdmissionRequest(ctx, c.review.Request)

			wh, _, err := NewWebhookNode(context.TODO(), controlCtx, nil, nil, nil)
			assert.NoError(t, err)

			gotResponse := wh.Review(ctx, c.review)
			assert.Equal(t, gotResponse.Allowed, c.allow)
			assert.Equal(t, gotResponse.UID, c.review.Request.UID)
			for i := range c.expPatch {
				assert.Contains(t, string(gotResponse.Patch), c.expPatch[i])
			}
		})
	}
}

func nodeToJson(node *v1.Node) []byte {
	nj, _ := json.Marshal(node)
	return nj
}
