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

package cpusettopology

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func emptyLifecycleMetaServer() *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{PodFetcher: &metapod.PodFetcherStub{}},
	}
}

// appliedViewFromFinalSnapshot is a test-only thin wrapper over
// appliedViewFromFinalSnapshotWithContext using a background context.
func appliedViewFromFinalSnapshot(
	metaServer *metaserver.MetaServer,
	desired *model.DesiredView,
	dag *topology.TopoDAG,
	snapshot *topology.CompleteSnapshot,
	expectedCPUSetByRel ...map[string]machine.CPUSet,
) (*model.AppliedView, error) {
	proofs, err := freezeContainerLifecycleProofs(nil, nil)
	if err != nil {
		return nil, err
	}
	if metaServer == nil {
		metaServer = emptyLifecycleMetaServer()
	}
	return appliedViewFromFinalSnapshotWithContext(
		context.Background(), metaServer, desired, dag, snapshot, proofs, nil)
}
