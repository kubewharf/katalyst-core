//go:build integration_test
// +build integration_test

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

package podadmit

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func Test_Pod_Admit_Service_Integration(t *testing.T) {
	t.Logf("lengthy integration test; not intended as part of check-in tests")

	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	manager := &admitter{
		qosConfig:     generic.NewQoSConfiguration(),
		domainManager: &mbdomain.MBDomainManager{},
	}
	pluginapi.RegisterResourcePluginServer(s, manager)
	go func() {
		if err := s.Serve(lis); err != nil {
			t.Errorf("failed to serve: %v", err)
		}
	}()

	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Errorf("dial: %v", err)
	}

	client := pluginapi.NewResourcePluginClient(conn)
	resp, err := client.AllocateForPod(context.Background(), &pluginapi.PodResourceRequest{
		PodUid:       "pod-123-4567",
		PodNamespace: "ns-test",
		PodName:      "pod-test",
		PodRole:      "",
		PodType:      "",
		ResourceName: "",
		Hint: &pluginapi.TopologyHint{
			Nodes:     []uint64{2},
			Preferred: true,
		},
		ResourceRequests: nil,
		Labels:           nil,
		Annotations: map[string]string{
			"katalyst.kubewharf.io/qos_level": "dedicated_cores",
		},
	})

	assert.NoError(t, err)
	t.Logf("response: %#v", resp)
}
