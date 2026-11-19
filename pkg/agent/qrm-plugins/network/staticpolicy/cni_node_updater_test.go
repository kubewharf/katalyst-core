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

package staticpolicy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/cni"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cniFetcherFunc func(context.Context) (cni.CNIInfo, error)

func (f cniFetcherFunc) FetchCNIInfo(ctx context.Context) (cni.CNIInfo, error) { return f(ctx) }

type cniTestNICManager struct{}

func (cniTestNICManager) GetNICs() nic.NICs   { return nic.NICs{} }
func (cniTestNICManager) Run(context.Context) {}

func TestWithCNIPeriodicNodeUpdater(t *testing.T) {
	t.Parallel()
	updater, err := cni.NewCNIPeriodicNodeUpdater(cniFetcherFunc(func(context.Context) (cni.CNIInfo, error) { return cni.CNIInfo{}, nil }), fake.NewSimpleClientset(), "node-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		plugin    skeleton.QRMPlugin
		updater   *cni.CNIPeriodicNodeUpdater
		wantError bool
	}{
		{"nil plugin", nil, updater, true},
		{"typed nil policy", (*StaticPolicy)(nil), updater, true},
		{"unsupported plugin", &skeleton.DummyQRMPlugin{}, updater, true},
		{"started policy", &StaticPolicy{started: true}, updater, true},
		{"enabled", &StaticPolicy{}, updater, false},
		{"disabled", &StaticPolicy{}, nil, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := WithCNIPeriodicNodeUpdater(tc.plugin, tc.updater)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantError && tc.plugin.(*StaticPolicy).cniNodeUpdater != tc.updater {
				t.Fatal("updater not retained")
			}
		})
	}
}

func TestCNIPolicyRegistration(t *testing.T) {
	t.Parallel()
	client := fake.NewSimpleClientset(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	patched := make(chan struct{}, 1)
	client.PrependReactor("patch", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched <- struct{}{}
		return false, nil, nil
	})
	updater, err := cni.NewCNIPeriodicNodeUpdater(cniFetcherFunc(func(context.Context) (cni.CNIInfo, error) {
		return cni.CNIInfo{AllocatableDirectIPs: 200}, nil
	}), client, "node-1")
	if err != nil {
		t.Fatal(err)
	}
	p := &StaticPolicy{name: t.Name(), emitter: metrics.DummyMetrics{}, nicManager: cniTestNICManager{}}
	if err := WithCNIPeriodicNodeUpdater(p, updater); err != nil {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("injection accessed Node API")
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	if general.GetRegisterReadinessCheckResult()[general.HealthzCheckName(cni.NodeUpdaterHandlerName)].Ready {
		t.Fatal("CNI check initially healthy")
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if err := WithCNIPeriodicNodeUpdater(p, nil); err == nil {
		t.Fatal("injection while running accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go (&periodicalhandler.PeriodicalHandlerManager{}).Run(ctx)
	select {
	case <-patched:
	case <-time.After(10 * time.Second):
		t.Fatal("StaticPolicy did not schedule the CNI Node update")
	}
	// Wait for the callback's health update, not just its API call.
	deadline := time.After(time.Second)
	for !general.GetRegisterReadinessCheckResult()[general.HealthzCheckName(cni.NodeUpdaterHandlerName)].Ready {
		select {
		case <-deadline:
			t.Fatal("successful publication not healthy")
		case <-time.After(time.Millisecond):
		}
	}
	node, err := client.CoreV1().Nodes().Get(context.Background(), "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quantity := node.Status.Capacity[consts.ResourcePodIPs]
	if quantity.Value() != 200 {
		t.Fatalf("capacity=%d, want 200", quantity.Value())
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if p.cniNodeUpdater != updater {
		t.Fatal("policy recreated the updater")
	}
	if err := WithCNIPeriodicNodeUpdater(p, nil); err != nil {
		t.Fatal(err)
	}
	if p.cniNodeUpdater != nil {
		t.Fatal("nil updater did not disable reporting")
	}
}

func TestCNIResourceNotReportedThroughQRM(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		count int64
		err   error
	}{
		{"success", 200, nil}, {"failure", 200, errors.New("CNI unavailable")},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := makeStaticPolicy(t, true)
			p.agentCtx.AllocatableInterfaceSocketInfo = &machine.AllocatableInterfaceSocketInfo{IfIndex2Sockets: map[int][]int{0: {0}, 2: {1}}}
			baseline, err := p.GetTopologyAwareAllocatableResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			updater, err := cni.NewCNIPeriodicNodeUpdater(cniFetcherFunc(func(context.Context) (cni.CNIInfo, error) { return cni.CNIInfo{AllocatableDirectIPs: tc.count}, tc.err }), fake.NewSimpleClientset(), "node-1")
			if err != nil {
				t.Fatal(err)
			}
			if err := WithCNIPeriodicNodeUpdater(p, updater); err != nil {
				t.Fatal(err)
			}
			resp, err := p.GetTopologyAwareAllocatableResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.AllocatableResources) != 1 || resp.AllocatableResources[string(consts.ResourceNetBandwidth)] == nil {
				t.Fatalf("expected bandwidth-only QRM response: %+v", resp)
			}
			if _, found := resp.AllocatableResources[string(consts.ResourcePodIPs)]; found {
				t.Fatal("CNI IP capacity reported through QRM")
			}
			if !reflect.DeepEqual(baseline, resp) {
				t.Fatal("updater injection changed bandwidth response")
			}
		})
	}
}
