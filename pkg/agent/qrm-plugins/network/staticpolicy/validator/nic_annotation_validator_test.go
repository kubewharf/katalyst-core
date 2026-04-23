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

package validator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type testPodFetcher struct {
	pod.PodFetcherStub

	podObj *v1.Pod
	err    error

	called  bool
	callCtx context.Context
	callUID string
}

func (t *testPodFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	t.called = true
	t.callCtx = ctx
	t.callUID = podUID

	if t.err != nil {
		return nil, t.err
	}
	return t.podObj, nil
}

func makeNetworkConfWithAnnotationKeys() *config.Configuration {
	conf := config.NewConfiguration()
	conf.NetworkQRMPluginConfig.IPv4ResourceAllocationAnnotationKey = "anno/ipv4"
	conf.NetworkQRMPluginConfig.IPv6ResourceAllocationAnnotationKey = "anno/ipv6"
	conf.NetworkQRMPluginConfig.NetNSPathResourceAllocationAnnotationKey = "anno/netns"
	conf.NetworkQRMPluginConfig.NetInterfaceNameResourceAllocationAnnotationKey = "anno/nic"
	conf.NetworkQRMPluginConfig.NetClassIDResourceAllocationAnnotationKey = "anno/netcls"
	conf.NetworkQRMPluginConfig.NetBandwidthResourceAllocationAnnotationKey = "anno/bw"
	return conf
}

func TestNICAnnotationValidator_NoForbiddenAnnotations(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	conf := makeNetworkConfWithAnnotationKeys()

	podObj := &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid", Namespace: "default", Name: "pod"}}

	v := NewNICAnnotationValidator(conf)
	ok, err := v.ValidatePodAnnotation(context.Background(), podObj.Annotations)
	as.True(ok)
	as.NoError(err)
}
