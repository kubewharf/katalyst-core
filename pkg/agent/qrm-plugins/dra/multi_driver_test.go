package dra

import (
	"context"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/stretchr/testify/require"
	resourcev1 "k8s.io/api/resource/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestPreparePersistsOnlyCurrentDriverAndIgnoresForeignOverlap(t *testing.T) {
	var dispatched []*resourcev1.ResourceClaim
	backend := &fakeProvider{prepareFunc: func(c *resourcev1.ResourceClaim) (PrepareResult, error) {
		dispatched = append(dispatched, c)
		return PrepareResult{}, nil
	}}
	p := newOperationTestPlugin(backend)
	claim := allocatedDeviceClaim("ns", "first", "first-uid", "gpu", false)
	claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, resourcev1.DeviceRequestAllocationResult{Driver: "other.example.com", Pool: "pool-a", Device: "foreign", Request: "other"})
	claim.Status.Allocation.Devices.Config = []resourcev1.DeviceAllocationConfiguration{{Requests: []string{"gpu", "other"}}, {Requests: []string{"other"}}, {DeviceConfiguration: resourcev1.DeviceConfiguration{Opaque: &resourcev1.OpaqueDeviceConfiguration{Driver: "other.example.com"}}}}
	_, err := p.prepareOne(context.Background(), claim)
	require.NoError(t, err)
	require.Len(t, dispatched[0].Status.Allocation.Devices.Results, 1)
	require.Len(t, dispatched[0].Status.Allocation.Devices.Config, 1)
	require.Equal(t, []string{"gpu"}, dispatched[0].Status.Allocation.Devices.Config[0].Requests)
	checkpoint, ok := p.state.GetPreparedClaim("first-uid")
	require.True(t, ok)
	require.Len(t, checkpoint.Status.Allocation.Devices.Results, 1)
	require.Len(t, claim.Status.Allocation.Devices.Results, 2, "must not mutate the informer/API claim")
	second := claim.DeepCopy()
	second.Name, second.UID = "second", "second-uid"
	second.Status.Allocation.Devices.Results[0].Device = "different-local-device"
	_, err = p.prepareOne(context.Background(), second)
	require.NoError(t, err, "overlap solely on the other driver's allocation must not block this driver")
	p.driverName = "unallocated.example.com"
	_, err = p.prepareOne(context.Background(), claim)
	require.Error(t, err)
	require.True(t, IsNonRetryable(err))
	require.Len(t, dispatched, 2)
}

func TestRestoredMixedCheckpointIsScopedBeforeBackendStart(t *testing.T) {
	conf := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}
	checkpoint, err := state.NewCheckpointStateWithName(conf, "restore-test")
	require.NoError(t, err)
	claim := allocatedDeviceClaim("ns", "claim", "uid", "gpu", false)
	claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, resourcev1.DeviceRequestAllocationResult{Driver: "other.example.com", Pool: "node", Device: "foreign"})
	require.NoError(t, checkpoint.SetPreparedClaim(state.NewPreparedClaim(claim, state.PrepareCompleted), true))
	recorder := &lifecycleRecorder{}
	backend := &lifecycleProvider{recorder: recorder}
	p, err := NewStaticPolicy(Options{DriverName: "test.dra.example.com", CheckpointName: "restore-test", StateDirectoryConfiguration: conf, KubeClient: kubefake.NewSimpleClientset(), Backend: backend, ResourceSlicePublisher: &lifecyclePublisher{recorder: recorder}})
	require.NoError(t, err)
	require.NoError(t, p.Start())
	require.NoError(t, p.Stop())
	require.Len(t, backend.restoredState["uid"].Status.Allocation.Devices.Results, 1)
	restored, err := state.NewCheckpointStateWithName(conf, "restore-test")
	require.NoError(t, err)
	prepared, exists := restored.GetPreparedClaim("uid")
	require.True(t, exists)
	require.Len(t, prepared.Status.Allocation.Devices.Results, 1)
}

func TestDriverCheckpointsAndCleanupNamesAreIndependent(t *testing.T) {
	conf := &statedirectory.StateDirectoryConfiguration{StateFileDirectory: t.TempDir()}
	a, err := state.NewCheckpointStateWithName(conf, "driver-a")
	require.NoError(t, err)
	b, err := state.NewCheckpointStateWithName(conf, "driver-b")
	require.NoError(t, err)
	claim := allocatedDeviceClaim("ns", "claim", "shared-uid", "gpu", false)
	require.NoError(t, a.SetPreparedClaim(state.NewPreparedClaim(claim, state.PrepareCompleted), true))
	_, exists := b.GetPreparedClaim("shared-uid")
	require.False(t, exists)
	restored, err := state.NewCheckpointStateWithName(conf, "driver-a")
	require.NoError(t, err)
	_, exists = restored.GetPreparedClaim("shared-uid")
	require.True(t, exists)
	_, err = state.NewCheckpointStateWithName(conf, "../escape")
	require.Error(t, err)
	pa, pb := &StaticPolicy{driverName: "a.example.com"}, &StaticPolicy{driverName: "b.example.com"}
	require.NotEqual(t, pa.cleanupHandlerName(), pb.cleanupHandlerName())
	require.NotEqual(t, pa.cleanupGroupName(), pb.cleanupGroupName())
}
