package dra

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
)

func publisherTestInventory(n int) Inventory {
	i := Inventory{PoolName: "node-a"}
	for k := 0; k < n; k++ {
		i.Devices = append(i.Devices, resourcev1.Device{Name: fmt.Sprintf("channel-%d", k)})
	}
	return i
}
func TestShardsGenerationAndOwnership(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	client := kubefake.NewSimpleClientset(node)
	cfg := &PublisherConfig{DriverName: "test.example.com", OwnerName: AgentName}
	cfg.NodeName = node.Name
	p := &Publisher{Config: cfg, Client: client}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	list, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 3)
	for _, s := range list.Items {
		require.LessOrEqual(t, len(s.Spec.Devices), 128)
		require.Equal(t, int64(3), s.Spec.Pool.ResourceSliceCount)
		require.Equal(t, int64(1), s.Spec.Pool.Generation)
		require.Equal(t, node.UID, s.OwnerReferences[0].UID)
	}
	before := len(client.Actions())
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	for _, a := range client.Actions()[before:] {
		require.NotEqual(t, "update", a.GetVerb())
	}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(1)))
	list, err = client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, int64(2), list.Items[0].Spec.Pool.Generation)
}
func TestForeignSlicePreventsConflictingPublication(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	foreign := BuildSlices(node, publisherTestInventory(1), 1, "test.example.com", AgentName)[0]
	foreign.Name = "nvidia-existing"
	foreign.Labels = nil
	client := kubefake.NewSimpleClientset(node, &foreign)
	cfg := &PublisherConfig{DriverName: "test.example.com", OwnerName: AgentName}
	cfg.NodeName = node.Name
	p := &Publisher{Config: cfg, Client: client}
	require.Error(t, p.Reconcile(context.Background(), publisherTestInventory(2)))
	for _, a := range client.Actions() {
		require.NotContains(t, []string{"create", "update", "delete"}, a.GetVerb())
	}
}

func TestNativeSliceAdoptionRetainsIdentityAndMetadata(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	native := BuildSlices(node, publisherTestInventory(2), 7, "test.example.com", "native")[0]
	native.Spec.Devices[0], native.Spec.Devices[1] = native.Spec.Devices[1], native.Spec.Devices[0]
	native.Name, native.GenerateName = "node-a-test.example.com-b6nm2", "node-a-test.example.com-"
	native.UID, native.ResourceVersion = "slice-uid", "123"
	native.Labels = nil
	native.Annotations = map[string]string{"tce.kubernetes.io/lastUpdate": "456"}
	controller := true
	native.OwnerReferences[0].Controller = &controller
	client := kubefake.NewSimpleClientset(node, &native)
	p := &Publisher{Config: &PublisherConfig{
		NodeName: node.Name, DriverName: "test.example.com", OwnerName: AgentName,
		PreserveExistingNames: true,
		AdoptExisting:         func(n *corev1.Node, s resourcev1.ResourceSlice) bool { return s.Spec.Pool.Name == n.Name },
	}, Client: client}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(2)))
	got, err := client.ResourceV1().ResourceSlices().Get(ctx, native.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, native.UID, got.UID)
	require.Equal(t, native.GenerateName, got.GenerateName)
	require.Equal(t, native.OwnerReferences, got.OwnerReferences)
	require.Equal(t, native.Spec.Pool.Generation, got.Spec.Pool.Generation)
	require.Equal(t, native.Spec.Devices, got.Spec.Devices)
	require.Equal(t, "456", got.Annotations["tce.kubernetes.io/lastUpdate"])
	require.Equal(t, AgentName, got.Labels[ManagedLabel])
	before := len(client.Actions())
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(2)))
	for _, a := range client.Actions()[before:] {
		require.NotContains(t, []string{"create", "update", "delete"}, a.GetVerb())
	}
	for _, a := range client.Actions() {
		require.NotContains(t, []string{"create", "delete"}, a.GetVerb())
	}
}

func TestAdoptionCannotBypassLiveNodeOwnership(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "current-node"}}
	native := BuildSlices(node, publisherTestInventory(1), 1, "test.example.com", "native")[0]
	native.Labels = nil
	native.OwnerReferences[0].UID = "deleted-node"
	client := kubefake.NewSimpleClientset(node, &native)
	p := &Publisher{Config: &PublisherConfig{NodeName: node.Name, DriverName: "test.example.com", OwnerName: AgentName,
		PreserveExistingNames: true, AdoptExisting: func(*corev1.Node, resourcev1.ResourceSlice) bool { return true },
	}, Client: client}
	require.ErrorContains(t, p.Reconcile(context.Background(), publisherTestInventory(1)), "different node owner")
	for _, a := range client.Actions() {
		require.NotContains(t, []string{"create", "update", "delete"}, a.GetVerb())
	}
}

func TestPublicationResumesPartiallyCommittedGeneration(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	client := kubefake.NewSimpleClientset(node)
	cfg := &PublisherConfig{DriverName: "test.example.com", OwnerName: AgentName}
	cfg.NodeName = node.Name
	p := &Publisher{Config: cfg, Client: client}
	count := 0
	client.PrependReactor("create", "resourceslices", func(action ktesting.Action) (bool, runtime.Object, error) {
		count++
		if count == 2 {
			return true, nil, fmt.Errorf("injected API failure")
		}
		return false, nil, nil
	})
	require.Error(t, p.Reconcile(ctx, publisherTestInventory(257)))
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	slices, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, slices.Items, 3)
	for _, s := range slices.Items {
		require.Equal(t, int64(1), s.Spec.Pool.Generation)
		require.Equal(t, int64(3), s.Spec.Pool.ResourceSliceCount)
	}
}

// Fake clients do not implement server-side GenerateName. Use non-lexical
// names so this also checks stable shard assignment after a process restart.
func generatedPublisherClient(node *corev1.Node, failAt int) *kubefake.Clientset {
	client := kubefake.NewSimpleClientset(node)
	count := 0
	client.PrependReactor("create", "resourceslices", func(action ktesting.Action) (bool, runtime.Object, error) {
		count++
		if count == failAt {
			return true, nil, fmt.Errorf("injected API failure")
		}
		slice := action.(ktesting.CreateAction).GetObject().(*resourcev1.ResourceSlice)
		if slice.Name == "" && slice.GenerateName != "" {
			suffix := []string{"zzzzz", "aaaaa", "mmmmm"}[(count-1)%3]
			slice.Name = fmt.Sprintf("%s%s-%d", slice.GenerateName, suffix, count)
		}
		return false, nil, nil
	})
	return client
}

func TestGeneratedSliceNamesPersistAcrossRestartAndResize(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	client := generatedPublisherClient(node, 0)
	cfg := &PublisherConfig{NodeName: node.Name, DriverName: "test.example.com", OwnerName: AgentName, UseGenerateName: true}
	p := &Publisher{Config: cfg, Client: client}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	list, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 3)
	names := map[string]string{}
	for _, slice := range list.Items {
		require.Equal(t, "node-a-test.example.com-", slice.GenerateName)
		require.Contains(t, slice.Name, slice.GenerateName)
		names[slice.Labels[ShardLabel]] = slice.Name
	}
	before := len(client.Actions())
	p = &Publisher{Config: cfg, Client: client}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	for _, action := range client.Actions()[before:] {
		require.NotContains(t, []string{"create", "update", "delete"}, action.GetVerb())
	}
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(1)))
	list, err = client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, names["0"], list.Items[0].Name)
	require.Equal(t, int64(2), list.Items[0].Spec.Pool.Generation)
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	before = len(client.Actions())
	require.NoError(t, (&Publisher{Config: cfg, Client: client}).Reconcile(ctx, publisherTestInventory(257)))
	for _, action := range client.Actions()[before:] {
		require.NotContains(t, []string{"create", "update", "delete"}, action.GetVerb())
	}
}

func TestGeneratedSlicesResumeAfterPartialCreate(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid"}}
	client := generatedPublisherClient(node, 2)
	p := &Publisher{Config: &PublisherConfig{NodeName: node.Name, DriverName: "test.example.com", OwnerName: AgentName, UseGenerateName: true}, Client: client}
	require.Error(t, p.Reconcile(ctx, publisherTestInventory(257)))
	partial, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, partial.Items, 1)
	originalName := partial.Items[0].Name
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	list, err := client.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 3)
	found := false
	for _, slice := range list.Items {
		if slice.Name == originalName {
			found = true
		}
		require.Equal(t, int64(1), slice.Spec.Pool.Generation)
	}
	require.True(t, found)
	before := len(client.Actions())
	require.NoError(t, p.Reconcile(ctx, publisherTestInventory(257)))
	for _, action := range client.Actions()[before:] {
		require.NotContains(t, []string{"create", "update", "delete"}, action.GetVerb())
	}
}
