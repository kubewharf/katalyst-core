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

package server

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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
)

func setupGrpcServer(ctx context.Context) (evictor.PodEvictor, evictionv1apha1.EvictionPluginClient, func()) {
	buffer := 101024 * 1024
	lis := bufconn.Listen(buffer)

	server := &powerPressureEvictServer{}
	baseServer := grpc.NewServer()
	evictionv1apha1.RegisterEvictionPluginServer(baseServer, server)
	server.started = true
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

	_ = evictServer.Evict(ctx, []*v1.Pod{{ObjectMeta: metav1.ObjectMeta{UID: "test123456"}}})

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

func Test_powerPressureEvictPluginServer_GetEvictPods(t *testing.T) {
	t.Parallel()

	type fields struct {
		evicts map[types.UID]*v1.Pod
	}
	type args struct {
		ctx     context.Context
		request *evictionv1apha1.GetEvictPodsRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *evictionv1apha1.GetEvictPodsResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "happy path of no evict",
			fields: fields{
				evicts: nil,
			},
			args: args{
				ctx: context.TODO(),
				request: &evictionv1apha1.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{
						{ObjectMeta: metav1.ObjectMeta{UID: "test1001"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
					},
				},
			},
			want: &evictionv1apha1.GetEvictPodsResponse{
				EvictPods: []*evictionv1apha1.EvictPod{},
				Condition: nil,
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
				request: &evictionv1apha1.GetEvictPodsRequest{
					ActivePods: []*v1.Pod{
						{ObjectMeta: metav1.ObjectMeta{UID: "test1001"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
						{ObjectMeta: metav1.ObjectMeta{UID: "test1003"}},
					},
				},
			},
			want: &evictionv1apha1.GetEvictPodsResponse{
				EvictPods: []*evictionv1apha1.EvictPod{
					{
						Pod:                &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "test1002"}},
						Reason:             "host under power pressure",
						ForceEvict:         true,
						EvictionPluginName: "node_power_pressure",
					},
					{
						Pod:                &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "test1003"}},
						Reason:             "host under power pressure",
						ForceEvict:         true,
						EvictionPluginName: "node_power_pressure",
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerPressureEvictServer{
				evicts: tt.fields.evicts,
			}
			got, err := p.GetEvictPods(tt.args.ctx, tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetTopEvictionPods(%v, %v)", tt.args.ctx, tt.args.request)) {
				return
			}

			// making test result sorted so to get consistent compares
			sort.Slice(got.EvictPods, func(i, j int) bool {
				return got.EvictPods[i].Pod.UID < got.EvictPods[j].Pod.UID
			})
			assert.Equalf(t, tt.want.EvictPods, got.EvictPods, "GetTopEvictionPods(%v, %v)", tt.args.ctx, tt.args.request)
		})
	}
}
