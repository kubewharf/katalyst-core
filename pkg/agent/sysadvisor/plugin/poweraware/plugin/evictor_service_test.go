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

package plugin

import (
	"context"
	"fmt"
	"net"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	evictionv1apha1 "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
)

func setupGrpcServer(ctx context.Context) (PodEvictor, evictionv1apha1.EvictionPluginClient, func()) {
	buffer := 101024 * 1024
	lis := bufconn.Listen(buffer)

	server := &powerPressureEvictPluginServer{}
	baseServer := grpc.NewServer()
	evictionv1apha1.RegisterEvictionPluginServer(baseServer, server)
	go func() {
		if err := baseServer.Serve(lis); err != nil {
			fmt.Printf("error serving server: %v/n", err)
		}
	}()

	conn, err := grpc.DialContext(ctx, "",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Printf("error connecting to server: %v/n", err)
	}

	closer := func() {
		err := lis.Close()
		if err != nil {
			fmt.Printf("error closing listener: %v/n", err)
		}
		baseServer.Stop()
	}

	client := evictionv1apha1.NewEvictionPluginClient(conn)

	return server, client, closer
}

func Test_powerPressureEvictPluginServer_GetEvictPods_OnWire(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	evictServer, client, closer := setupGrpcServer(ctx)
	defer closer()

	evictServer.Reset(ctx)
	_ = evictServer.Evict(ctx, &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "test123456"}})

	got, err := client.GetEvictPods(ctx, &evictionv1apha1.GetEvictPodsRequest{
		ActivePods: []*v1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				UID: "test123456",
			},
		}},
	})

	assert.NoError(t, err, "unexpected error")
	assert.Equal(t, 1, len(got.EvictPods))
	assert.Equal(t, types.UID("test123456"), got.EvictPods[0].Pod.GetUID())
}

func Test_powerPressureEvictPluginServer_ThresholdMet(t *testing.T) {
	t.Parallel()

	checkError := func(t assert.TestingT, err error, opts ...interface{}) bool {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return false
		}
		return true
	}

	type fields struct {
		evicts map[types.UID]*v1.Pod
	}
	type args struct {
		ctx   context.Context
		empty *evictionv1apha1.Empty
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *evictionv1apha1.ThresholdMetResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path for no evict",
			fields: fields{
				evicts: make(map[types.UID]*v1.Pod),
			},
			args: args{
				ctx:   context.TODO(),
				empty: nil,
			},
			want: &evictionv1apha1.ThresholdMetResponse{
				ThresholdValue: 0,
				ObservedValue:  0,
			},
			wantErr: checkError,
		},
		{
			name: "happy path for some evicts",
			fields: fields{
				evicts: map[types.UID]*v1.Pod{
					"1234": {
						ObjectMeta: metav1.ObjectMeta{
							UID: "1234",
						},
					},
				},
			},
			args: args{
				ctx:   context.TODO(),
				empty: nil,
			},
			want: &evictionv1apha1.ThresholdMetResponse{
				ThresholdValue: 1,
				ObservedValue:  0,
			},
			wantErr: checkError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerPressureEvictPluginServer{
				evicts: tt.fields.evicts,
			}
			got, err := p.ThresholdMet(tt.args.ctx, tt.args.empty)
			if !tt.wantErr(t, err, fmt.Sprintf("ThresholdMet(%v, %v)", tt.args.ctx, tt.args.empty)) {
				return
			}
			assert.Equalf(t, tt.want, got, "ThresholdMet(%v, %v)", tt.args.ctx, tt.args.empty)
		})
	}
}

func Test_powerPressureEvictPluginServer_GetTopEvictionPods(t *testing.T) {
	t.Parallel()

	type fields struct {
		evicts map[types.UID]*v1.Pod
	}
	type args struct {
		ctx     context.Context
		request *evictionv1apha1.GetTopEvictionPodsRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *evictionv1apha1.GetTopEvictionPodsResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path of no evict",
			fields: fields{
				evicts: nil,
			},
			args: args{
				ctx: context.TODO(),
				request: &evictionv1apha1.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{
						{ObjectMeta: metav1.ObjectMeta{UID: "test1001"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
					},
					TopN: 1,
				},
			},
			want: &evictionv1apha1.GetTopEvictionPodsResponse{
				TargetPods: []*v1.Pod{},
			},
			wantErr: func(t assert.TestingT, err error, opts ...interface{}) bool {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return false
				}
				return true
			},
		},
		{
			name: "happy path of some evicts",
			fields: fields{
				evicts: map[types.UID]*v1.Pod{
					"test1002": {ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
					"test9999": {ObjectMeta: metav1.ObjectMeta{UID: "test9999"}},
					"test1003": {ObjectMeta: metav1.ObjectMeta{UID: "test1003"}},
				},
			},
			args: args{
				ctx: context.TODO(),
				request: &evictionv1apha1.GetTopEvictionPodsRequest{
					ActivePods: []*v1.Pod{
						{ObjectMeta: metav1.ObjectMeta{UID: "test1001"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1003"}},
					},
					TopN: 2,
				},
			},
			want: &evictionv1apha1.GetTopEvictionPodsResponse{
				TargetPods: []*v1.Pod{
					{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
					{ObjectMeta: metav1.ObjectMeta{UID: "test1003"}},
				},
			},
			wantErr: func(t assert.TestingT, err error, opts ...interface{}) bool {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return false
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerPressureEvictPluginServer{
				evicts: tt.fields.evicts,
			}
			got, err := p.GetTopEvictionPods(tt.args.ctx, tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetTopEvictionPods(%v, %v)", tt.args.ctx, tt.args.request)) {
				return
			}

			// making test result sorted so to get consistent compares
			sort.Slice(got.TargetPods, func(i, j int) bool {
				return got.TargetPods[i].UID < got.TargetPods[j].UID
			})
			assert.Equalf(t, tt.want.TargetPods, got.TargetPods, "GetTopEvictionPods(%v, %v)", tt.args.ctx, tt.args.request)
		})
	}
}
