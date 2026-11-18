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

package dra

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	kubefake "k8s.io/client-go/kubernetes/fake"
	api "k8s.io/kubelet/pkg/apis/dra/v1"
	beta "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	watcher "k8s.io/kubelet/pkg/apis/pluginregistration/v1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

func TestNewStaticPolicyRequiresRegistrationDirectory(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{"", "relative"} {
		directory := directory
		t.Run("directory="+directory, func(t *testing.T) {
			t.Parallel()
			_, err := NewStaticPolicy(Options{RegistrationDir: directory})
			require.ErrorContains(t, err, "registration directory must be absolute")
		})
	}
}

func TestNewStaticPolicyReturnsSharedWrapper(t *testing.T) {
	t.Parallel()
	options := Options{
		RegistrationDir:             "/tmp",
		Backend:                     &testBackend{},
		ResourceSlicePublisher:      &lifecyclePublisher{recorder: &lifecycleRecorder{}},
		KubeClient:                  kubefake.NewSimpleClientset(),
		StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()},
	}
	wrapper, err := NewStaticPolicy(options)
	require.NoError(t, err)
	var _ *skeleton.PluginRegistrationWrapper = wrapper
	require.Equal(t, DefaultDriverName, wrapper.Name())
	options.Backend = nil
	_, err = NewStaticPolicy(options)
	require.ErrorContains(t, err, "backend")
}

type registrationProbe struct {
	api.UnimplementedDRAPluginServer
	prepares   atomic.Int32
	unprepares atomic.Int32
}

func (*registrationProbe) Name() string { return "test.example.com" }
func (*registrationProbe) Start() error { return nil }
func (*registrationProbe) Stop() error  { return nil }
func (p *registrationProbe) NodePrepareResources(context.Context, *api.NodePrepareResourcesRequest) (*api.NodePrepareResourcesResponse, error) {
	p.prepares.Add(1)
	return &api.NodePrepareResourcesResponse{}, nil
}

func (p *registrationProbe) NodeUnprepareResources(context.Context, *api.NodeUnprepareResourcesRequest) (*api.NodeUnprepareResourcesResponse, error) {
	p.unprepares.Add(1)
	return &api.NodeUnprepareResourcesResponse{}, nil
}

func TestSharedDRARegistration(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"v1", "v1beta1"} {
		version := version
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			root, err := os.MkdirTemp("/tmp", "dra-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
			policy := &registrationProbe{}
			wrapper, err := skeleton.NewRegistrationPluginWrapper(policy, []string{root}, nil)
			require.NoError(t, err)
			require.NoError(t, wrapper.Start())
			t.Cleanup(func() { require.NoError(t, wrapper.Stop()) })
			conn, err := grpc.NewClient("unix:"+filepath.Join(root, policy.Name()+".sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			info, err := watcher.NewRegistrationClient(conn).GetInfo(ctx, &watcher.InfoRequest{}, grpc.WaitForReady(true))
			require.NoError(t, err)
			require.Equal(t, watcher.DRAPlugin, info.Type)
			require.Equal(t, policy.Name(), info.Name)
			require.ElementsMatch(t, []string{api.DRAPluginService, beta.DRAPluginService}, info.SupportedVersions)
			require.Empty(t, info.Endpoint)
			_, err = watcher.NewRegistrationClient(conn).NotifyRegistrationStatus(ctx, &watcher.RegistrationStatus{PluginRegistered: true})
			require.NoError(t, err)
			if version == "v1" {
				client := api.NewDRAPluginClient(conn)
				_, err = client.NodePrepareResources(ctx, &api.NodePrepareResourcesRequest{})
				require.NoError(t, err)
				_, err = client.NodeUnprepareResources(ctx, &api.NodeUnprepareResourcesRequest{})
				require.NoError(t, err)
			} else {
				client := beta.NewDRAPluginClient(conn)
				_, err = client.NodePrepareResources(ctx, &beta.NodePrepareResourcesRequest{})
				require.NoError(t, err)
				_, err = client.NodeUnprepareResources(ctx, &beta.NodeUnprepareResourcesRequest{})
				require.NoError(t, err)
			}
			require.EqualValues(t, 1, policy.prepares.Load())
			require.EqualValues(t, 1, policy.unprepares.Load())
		})
	}
}
