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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
)

func TestRealNodeUpdater_PatchNodeStatus(t *testing.T) {
	t.Parallel()

	type fields struct {
		oldNode *v1.Node
	}
	type args struct {
		newNode *v1.Node
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantNode *v1.Node
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			name: "patch resources",
			fields: fields{
				oldNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
				},
			},
			args: args{
				newNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Status: v1.NodeStatus{
						Capacity: v1.ResourceList{
							"resource-1": resource.MustParse("10"),
						},
					},
				},
			},
			wantNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
				},
				Status: v1.NodeStatus{
					Capacity: v1.ResourceList{
						"resource-1": resource.MustParse("10"),
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.fields.oldNode})
			assert.NoError(t, err)
			r := NewRealNodeUpdater(baseCtx.Client.KubeClient)
			tt.wantErr(t, r.PatchNodeStatus(context.Background(), tt.fields.oldNode, tt.args.newNode), fmt.Sprintf("PatchNodeStatus(%v, %v)", tt.fields.oldNode, tt.args.newNode))

			getNode, err := baseCtx.Client.KubeClient.CoreV1().Nodes().Get(context.Background(), tt.fields.oldNode.Name, metav1.GetOptions{ResourceVersion: "0"})
			assert.NoError(t, err)
			assert.Equal(t, tt.wantNode, getNode)
		})
	}
}

func TestRealNodeUpdater_PatchNode(t *testing.T) {
	t.Parallel()

	type fields struct {
		oldNode *v1.Node
	}
	type args struct {
		newNode *v1.Node
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantNode *v1.Node
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			name: "patch taint",
			fields: fields{
				oldNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
				},
			},
			args: args{
				newNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Spec: v1.NodeSpec{
						Taints: []v1.Taint{
							{
								Key:    "key-1",
								Value:  "value-1",
								Effect: v1.TaintEffectNoSchedule,
							},
						},
					},
				},
			},
			wantNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
				},
				Spec: v1.NodeSpec{
					Taints: []v1.Taint{
						{
							Key:    "key-1",
							Value:  "value-1",
							Effect: v1.TaintEffectNoSchedule,
						},
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.fields.oldNode})
			assert.NoError(t, err)
			r := NewRealNodeUpdater(baseCtx.Client.KubeClient)
			tt.wantErr(t, r.PatchNode(context.Background(), tt.fields.oldNode, tt.args.newNode), fmt.Sprintf("PatchNodeStatus(%v, %v)", tt.fields.oldNode, tt.args.newNode))

			getNode, err := baseCtx.Client.KubeClient.CoreV1().Nodes().Get(context.Background(), tt.fields.oldNode.Name, metav1.GetOptions{ResourceVersion: "0"})
			assert.NoError(t, err)
			assert.Equal(t, tt.wantNode, getNode)
		})
	}
}

func TestRealNodeUpdater_UpdateNode(t *testing.T) {
	t.Parallel()

	type fields struct {
		oldNode *v1.Node
	}
	type args struct {
		newNode *v1.Node
		opt     metav1.UpdateOptions
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantNode *v1.Node
		wantErr  assert.ErrorAssertionFunc
	}{
		{
			name: "update taint",
			fields: fields{
				oldNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
				},
			},
			args: args{
				newNode: &v1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node-1",
					},
					Spec: v1.NodeSpec{
						Taints: []v1.Taint{
							{
								Key:    "key-1",
								Value:  "value-1",
								Effect: v1.TaintEffectNoSchedule,
							},
						},
					},
				},
			},
			wantNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
				},
				Spec: v1.NodeSpec{
					Taints: []v1.Taint{
						{
							Key:    "key-1",
							Value:  "value-1",
							Effect: v1.TaintEffectNoSchedule,
						},
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.fields.oldNode})
			assert.NoError(t, err)
			r := NewRealNodeUpdater(baseCtx.Client.KubeClient)
			getNode, err := r.UpdateNode(context.Background(), tt.args.newNode, tt.args.opt)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantNode, getNode)
		})
	}
}
