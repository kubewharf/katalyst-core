package dra

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/dra/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	api "k8s.io/kubelet/pkg/apis/dra/v1"
	registration "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
)

type testBackend struct {
	mu         sync.Mutex
	driver     string
	prepared   int
	unprepared int
}

func (*testBackend) Start(context.Context, StateReader) error { return nil }
func (*testBackend) Stop() error                              { return nil }
func (*testBackend) Inventory(context.Context) (Inventory, error) {
	return Inventory{PoolName: "node", Devices: []resourcev1.Device{{Name: "device"}}}, nil
}
func (b *testBackend) Prepare(_ context.Context, c *resourcev1.ResourceClaim) (PrepareResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepared++
	for _, result := range c.Status.Allocation.Devices.Results {
		if result.Driver != b.driver {
			return PrepareResult{}, errors.New("foreign allocation dispatched")
		}
	}
	return PrepareResult{Devices: []state.PreparedDevice{{PoolName: "node", DeviceName: "device", Requests: []string{"request"}, CDIDeviceIDs: []string{b.driver + "/device=one"}}}, ProviderState: json.RawMessage(`{"fake":true}`)}, nil
}
func (b *testBackend) Unprepare(_ context.Context, c state.PreparedClaim) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unprepared++
	for _, result := range c.Status.Allocation.Devices.Results {
		if result.Driver != b.driver {
			return errors.New("foreign checkpoint dispatched")
		}
	}
	return nil
}

func TestTwoDriversRegisterAndServeIndependentUnixEndpoints(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "dra-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	drivers := []string{"a.example.com", "b.example.com"}
	claim := &resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "claim", UID: "claim-uid"}, Status: resourcev1.ResourceClaimStatus{Allocation: &resourcev1.AllocationResult{Devices: resourcev1.DeviceAllocationResult{Results: []resourcev1.DeviceRequestAllocationResult{{Driver: drivers[0], Pool: "node", Device: "device", Request: "request"}, {Driver: drivers[1], Pool: "node", Device: "device", Request: "request"}}}}}}
	kube := kubefake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}}, claim)
	backends := map[string]*testBackend{}
	factory := func(config PluginConfig, runtime Runtime) (skeleton.GenericPlugin, error) {
		backend := &testBackend{driver: config.DriverName}
		backends[config.DriverName] = backend
		publisher := &Publisher{Config: &PublisherConfig{NodeName: "node", DriverName: config.DriverName, OwnerName: AgentName, ReconcilePeriod: time.Hour}, Client: kube, Inventory: backend.Inventory}
		policy, err := NewStaticPolicy(Options{DriverName: config.DriverName, Backend: backend, ResourceSlicePublisher: publisher, KubeClient: kube, StateDirectoryConfiguration: &statedirectory.StateDirectoryConfiguration{StateFileDirectory: config.StateDir}})
		if err != nil {
			return nil, err
		}
		return NewRegisteredPlugin(policy, runtime.RegistrationDir), nil
	}
	config := Config{RegistrationDir: root, StateDir: filepath.Join(root, "state")}
	for i, driver := range drivers {
		config.Plugins = append(config.Plugins, PluginConfig{ID: []string{"a", "b"}[i], Provider: "fake", Enabled: true, Required: true, DriverName: driver})
	}
	m, err := NewManagerWithProviders(config, Runtime{NodeName: "node"}, map[string]ProviderFactory{"fake": factory})
	require.NoError(t, err)
	require.NoError(t, m.Start())
	t.Cleanup(func() { require.NoError(t, m.Stop()) })
	endpoints := map[string]*grpc.ClientConn{}
	for _, driver := range drivers {
		conn, err := grpc.NewClient("unix:"+filepath.Join(root, driver+".sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		info, err := registration.NewRegistrationClient(conn).GetInfo(ctx, &registration.InfoRequest{})
		require.NoError(t, err)
		require.Equal(t, registration.DRAPlugin, info.Type)
		require.Equal(t, driver, info.Name)
		require.Equal(t, filepath.Join(root, driver+".sock"), info.Endpoint)
		_, err = registration.NewRegistrationClient(conn).NotifyRegistrationStatus(ctx, &registration.RegistrationStatus{PluginRegistered: true})
		require.NoError(t, err)
		cancel()
		endpoints[info.Name] = conn
	}
	request := &api.NodePrepareResourcesRequest{Claims: []*api.Claim{{Namespace: "ns", Name: "claim", Uid: "claim-uid"}}}
	for _, driver := range drivers {
		response, err := api.NewDRAPluginClient(endpoints[driver]).NodePrepareResources(context.Background(), request)
		require.NoError(t, err)
		require.Empty(t, response.Claims["claim-uid"].Error)
		require.Equal(t, driver+"/device=one", response.Claims["claim-uid"].Devices[0].CdiDeviceIds[0])
	}
	slices, err := kube.ResourceV1().ResourceSlices().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, slices.Items, 2)
	require.NotEqual(t, slices.Items[0].Name, slices.Items[1].Name)
	for _, driver := range drivers {
		response, err := api.NewDRAPluginClient(endpoints[driver]).NodeUnprepareResources(context.Background(), &api.NodeUnprepareResourcesRequest{Claims: request.Claims})
		require.NoError(t, err)
		require.Empty(t, response.Claims["claim-uid"].Error)
		require.Equal(t, 1, backends[driver].prepared)
		require.Equal(t, 1, backends[driver].unprepared)
	}
	require.NoError(t, m.Stop())
	for _, driver := range drivers {
		_, err := os.Stat(filepath.Join(root, driver+".sock"))
		require.True(t, os.IsNotExist(err))
	}
}

type testPlugin struct {
	name     string
	startErr error
	stopped  int
	stopErr  error
}

func (p *testPlugin) Name() string { return p.name }
func (p *testPlugin) Start() error { return p.startErr }
func (p *testPlugin) Stop() error  { p.stopped++; return p.stopErr }

func TestManagerRejectsDuplicatesAndRollsBackRequiredFailure(t *testing.T) {
	config := Config{RegistrationDir: "/tmp", StateDir: "/tmp/dra-state", Plugins: []PluginConfig{{ID: "first", Provider: "fake", Enabled: true, Required: true, DriverName: "a.example.com"}, {ID: "second", Provider: "fake", Enabled: true, Required: true, DriverName: "b.example.com"}}}
	a := &testPlugin{name: "a.example.com"}
	b := &testPlugin{name: "b.example.com", startErr: errors.New("failure")}
	factories := map[string]ProviderFactory{"fake": func(c PluginConfig, _ Runtime) (skeleton.GenericPlugin, error) {
		if c.ID == "first" {
			return a, nil
		}
		return b, nil
	}}
	m, err := NewManagerWithProviders(config, Runtime{}, factories)
	require.NoError(t, err)
	require.Error(t, m.Start())
	require.Equal(t, 1, a.stopped)
	config.Plugins[1].Required = false
	m, err = NewManagerWithProviders(config, Runtime{}, factories)
	require.NoError(t, err)
	require.NoError(t, m.Start())
	require.Len(t, m.active, 1)
	require.NoError(t, m.Stop())
	config.Plugins[1].DriverName = config.Plugins[0].DriverName
	_, err = NewManagerWithProviders(config, Runtime{}, factories)
	require.Error(t, err)
}

func TestUnsupportedAutodetectAndStrictConfiguration(t *testing.T) {
	config := Config{RegistrationDir: "/tmp", StateDir: "/tmp/dra-state", Plugins: []PluginConfig{{ID: "fake", Enabled: true, AutoDetect: true, DriverName: "a.example.com"}}}
	factories := map[string]ProviderFactory{"fake": func(PluginConfig, Runtime) (skeleton.GenericPlugin, error) { return nil, ErrUnsupportedNode }}
	m, err := NewManagerWithProviders(config, Runtime{}, factories)
	require.NoError(t, err)
	require.NoError(t, m.Start())
	require.Empty(t, m.active)
	require.NoError(t, m.Stop())
	config.Plugins[0].Required = true
	m, err = NewManagerWithProviders(config, Runtime{}, factories)
	require.NoError(t, err)
	require.Error(t, m.Start())
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("registrationDir: /tmp\nstateDir: /tmp/state\nplugins: []\n"), 0600))
	_, err = LoadConfig(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("stateDir: /tmp/state\nunknownField: true\n"), 0600))
	_, err = LoadConfig(path)
	require.Error(t, err)
}

func TestInjectedFactoriesAndConfigurationAreSnapshots(t *testing.T) {
	config := Config{RegistrationDir: "/tmp", StateDir: "/tmp/dra-state", Plugins: []PluginConfig{{ID: "fake", Enabled: true, Required: true, DriverName: DefaultDriverName, Config: map[string]json.RawMessage{"option": json.RawMessage(`"original"`)}}}}
	var resolved PluginConfig
	factories := map[string]ProviderFactory{"fake": func(p PluginConfig, _ Runtime) (skeleton.GenericPlugin, error) {
		resolved = p
		return &testPlugin{name: p.DriverName}, nil
	}}
	manager, err := NewManagerWithProviders(config, Runtime{}, factories)
	require.NoError(t, err)
	require.Empty(t, config.Plugins[0].StateDir)
	delete(factories, "fake")
	config.Plugins[0].Config["option"][1] = 'x'
	config.Plugins[0].DriverName = "changed.example.com"
	require.NoError(t, manager.Start())
	require.Equal(t, DefaultDriverName, resolved.DriverName)
	require.Equal(t, filepath.Join(config.StateDir, DefaultDriverName), resolved.StateDir)
	require.JSONEq(t, `"original"`, string(resolved.Config["option"]))
	require.NoError(t, manager.Stop())
}

func TestIncompleteShutdownBlocksRestartUntilCleanupSucceeds(t *testing.T) {
	config := Config{RegistrationDir: "/tmp", StateDir: "/tmp/dra-state", Plugins: []PluginConfig{{ID: "fake", Enabled: true, Required: true, DriverName: "a.example.com"}}}
	plugin := &testPlugin{name: "a.example.com", stopErr: errors.New("cleanup failure")}
	manager, err := NewManagerWithProviders(config, Runtime{}, map[string]ProviderFactory{"fake": func(PluginConfig, Runtime) (skeleton.GenericPlugin, error) { return plugin, nil }})
	require.NoError(t, err)
	require.NoError(t, manager.Start())
	require.Error(t, manager.Stop())
	require.Error(t, manager.Start())
	plugin.stopErr = nil
	require.NoError(t, manager.Stop())
	require.NoError(t, manager.Start())
	require.NoError(t, manager.Stop())
}
