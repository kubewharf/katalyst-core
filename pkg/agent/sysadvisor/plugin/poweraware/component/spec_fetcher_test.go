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

package component

import (
	"context"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
)

type mockNodeFetcher struct {
	node.NodeFetcher
}

func (m mockNodeFetcher) GetNode(ctx context.Context) (*v1.Node, error) {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"power_alert":       "s0",
				"power_budget":      "128",
				"power_internal_op": "8",
				"power_alert_time":  "2024-06-01T19:15:58Z",
			},
		},
	}, nil
}

var _ node.NodeFetcher = &mockNodeFetcher{}

func Test_specFetcherByNodeAnnotation_GetPowerSpec(t *testing.T) {
	t.Parallel()
	type fields struct {
		nodeFetcher node.NodeFetcher
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *types.PowerSpec
		wantErr bool
	}{
		{
			name: "happy path no error",
			fields: fields{
				nodeFetcher: &mockNodeFetcher{},
			},
			args: args{
				ctx: context.TODO(),
			},
			want: &types.PowerSpec{
				Alert:      types.PowerAlertS0,
				Budget:     128,
				InternalOp: types.InternalOpPause,
				AlertTime:  time.Date(2024, time.June, 1, 19, 15, 58, 0, time.UTC),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specFetcherByNodeAnnotation{
				nodeFetcher: tt.fields.nodeFetcher,
			}
			got, err := s.GetPowerSpec(tt.args.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetPowerSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetPowerSpec() got = %v, want %v", got, tt.want)
			}
		})
	}
}
