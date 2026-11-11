// Copyright 2026 The Katalyst Authors. SPDX-License-Identifier: Apache-2.0
package dra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

const AgentName = "qrm-dra-manager"

type Config struct {
	RegistrationDir string         `json:"registrationDir"`
	StateDir        string         `json:"stateDir"`
	Plugins         []PluginConfig `json:"plugins"`
}

type PluginConfig struct {
	ID         string                     `json:"id"`
	Provider   string                     `json:"provider,omitempty"`
	Enabled    bool                       `json:"enabled"`
	Required   bool                       `json:"required"`
	AutoDetect bool                       `json:"autoDetect"`
	DriverName string                     `json:"driverName"`
	StateDir   string                     `json:"stateDir,omitempty"`
	Config     map[string]json.RawMessage `json:"config,omitempty"`
}

type Runtime struct {
	NodeName        string
	RESTConfig      *rest.Config
	RegistrationDir string
}

type ProviderFactory func(PluginConfig, Runtime) (skeleton.GenericPlugin, error)

var providerRegistry = struct {
	sync.Mutex
	factories map[string]ProviderFactory
}{factories: map[string]ProviderFactory{}}

// RegisterProvider is intended for provider package init functions.
func RegisterProvider(name string, factory ProviderFactory) {
	providerRegistry.Lock()
	defer providerRegistry.Unlock()
	if factory == nil || providerRegistry.factories[name] != nil {
		panic("invalid or duplicate DRA provider: " + name)
	}
	providerRegistry.factories[name] = factory
}

func LoadConfig(path string) (Config, error) {
	var config Config
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	data, err = yaml.YAMLToJSONStrict(data)
	if err != nil {
		return config, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return config, err
	}
	return config, nil
}

type Manager struct {
	mu        sync.Mutex
	config    Config
	runtime   Runtime
	factories map[string]ProviderFactory
	active    []skeleton.GenericPlugin
	started   bool
}

func NewManager(config Config, runtime Runtime) (*Manager, error) {
	providerRegistry.Lock()
	factories := map[string]ProviderFactory{}
	for name, factory := range providerRegistry.factories {
		factories[name] = factory
	}
	providerRegistry.Unlock()
	return NewManagerWithProviders(config, runtime, factories)
}

// NewManagerWithProviders uses only the injected factories. Callers can wrap a
// factory to supply vendor or environment-specific dependencies.
func NewManagerWithProviders(config Config, runtime Runtime, factories map[string]ProviderFactory) (*Manager, error) {
	config.Plugins = append([]PluginConfig(nil), config.Plugins...)
	for i := range config.Plugins {
		options := make(map[string]json.RawMessage, len(config.Plugins[i].Config))
		for key, value := range config.Plugins[i].Config {
			options[key] = append(json.RawMessage(nil), value...)
		}
		config.Plugins[i].Config = options
	}
	registry := make(map[string]ProviderFactory, len(factories))
	for name, factory := range factories {
		registry[name] = factory
	}
	factories = registry
	if !filepath.IsAbs(config.RegistrationDir) || !filepath.IsAbs(config.StateDir) {
		return nil, fmt.Errorf("DRA registrationDir and stateDir must be absolute")
	}
	ids, drivers, states := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range config.Plugins {
		p := &config.Plugins[i]
		if !p.Enabled {
			continue
		}
		if len(validation.IsDNS1123Label(p.ID)) != 0 || ids[p.ID] {
			return nil, fmt.Errorf("invalid or duplicate DRA plugin id %q", p.ID)
		}
		if len(validation.IsDNS1123Subdomain(p.DriverName)) != 0 || drivers[p.DriverName] {
			return nil, fmt.Errorf("invalid or duplicate DRA driver %q", p.DriverName)
		}
		if len(filepath.Join(config.RegistrationDir, p.DriverName+".sock")) > 107 {
			return nil, fmt.Errorf("DRA socket path exceeds Unix limit for %s", p.DriverName)
		}
		if p.Provider == "" {
			p.Provider = p.ID
		}
		if factories[p.Provider] == nil {
			return nil, fmt.Errorf("DRA provider %q is not compiled into this agent", p.Provider)
		}
		if p.StateDir == "" {
			p.StateDir = filepath.Join(config.StateDir, p.DriverName)
		}
		p.StateDir = filepath.Clean(p.StateDir)
		if !filepath.IsAbs(p.StateDir) || states[p.StateDir] {
			return nil, fmt.Errorf("invalid or shared DRA state directory %q", p.StateDir)
		}
		ids[p.ID], drivers[p.DriverName], states[p.StateDir] = true, true, true
	}
	runtime.RegistrationDir = config.RegistrationDir
	return &Manager{config: config, runtime: runtime, factories: factories}, nil
}

func (*Manager) Name() string { return AgentName }

// UnsupportedNode marks an explicit hardware probe failure. Optional autodetect
// providers may skip such nodes; unknown provider/configuration errors fail fast.
var ErrUnsupportedNode = errors.New("DRA provider unsupported on node")

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}
	if len(m.active) != 0 {
		return fmt.Errorf("previous DRA driver shutdown is incomplete")
	}
	for _, config := range m.config.Plugins {
		if !config.Enabled {
			continue
		}
		plugin, err := m.factories[config.Provider](config, m.runtime)
		if err != nil {
			if config.AutoDetect && !config.Required && errors.Is(err, ErrUnsupportedNode) {
				klog.InfoS("Skipping unsupported DRA provider", "driver", config.DriverName)
				continue
			}
			return errors.Join(fmt.Errorf("construct DRA driver %s: %w", config.DriverName, err), m.stop())
		}
		if plugin == nil || plugin.Name() != config.DriverName {
			return errors.Join(fmt.Errorf("provider %s returned a different DRA driver", config.Provider), m.stop())
		}
		if err = plugin.Start(); err != nil {
			if stopErr := plugin.Stop(); stopErr != nil {
				m.active = append(m.active, plugin)
				return errors.Join(err, stopErr, m.stop())
			}
			if config.Required {
				return errors.Join(err, m.stop())
			}
			klog.ErrorS(err, "Skipping optional DRA driver after startup failure", "driver", config.DriverName)
			continue
		}
		m.active = append(m.active, plugin)
	}
	m.started = true
	return nil
}

func (m *Manager) Stop() error { m.mu.Lock(); defer m.mu.Unlock(); return m.stop() }

func (m *Manager) stop() error {
	var result error
	remaining := []skeleton.GenericPlugin{}
	for i := len(m.active) - 1; i >= 0; i-- {
		if err := m.active[i].Stop(); err != nil {
			result = errors.Join(result, err)
			remaining = append(remaining, m.active[i])
		}
	}
	// Keep configuration order so each retry still shuts down in reverse order.
	for i, j := 0, len(remaining)-1; i < j; i, j = i+1, j-1 {
		remaining[i], remaining[j] = remaining[j], remaining[i]
	}
	m.active = remaining
	m.started = false
	return result
}
