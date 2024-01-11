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

package orm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	endpoint2 "github.com/kubewharf/katalyst-core/pkg/agent/orm/endpoint"
)

func (m *ManagerImpl) removeContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		filePath := filepath.Join(dir, name)
		if filePath == m.checkpointFile() {
			continue
		}
		stat, err := os.Stat(filePath)
		if err != nil {
			klog.Errorf("[ORM] Failed to stat file %s: %v", filePath, err)
			continue
		}
		if stat.IsDir() {
			continue
		}
		err = os.RemoveAll(filePath)
		if err != nil {
			errs = append(errs, err)
			klog.Errorf("[ORM] Failed to remove file %s: %v", filePath, err)
			continue
		}
	}
	return errors.NewAggregate(errs)
}

// ValidatePlugin validates a plugin if the version is correct and the name has the format of an extended resource
func (m *ManagerImpl) ValidatePlugin(pluginName string, endpoint string, versions []string) error {
	klog.V(2).Infof("Got Plugin %s at endpoint %s with versions %v", pluginName, endpoint, versions)

	if !m.isVersionCompatibleWithPlugin(versions) {
		return fmt.Errorf("manager version, %s, is not among plugin supported versions %v", pluginapi.Version, versions)
	}

	return nil
}

// RegisterPlugin starts the endpoint and registers it
func (m *ManagerImpl) RegisterPlugin(pluginName string, endpoint string, versions []string) error {
	klog.V(2).Infof("[ORM] Registering Plugin %s at endpoint %s", pluginName, endpoint)

	e, err := endpoint2.NewEndpointImpl(endpoint, pluginName)
	if err != nil {
		return fmt.Errorf("[ORM] failed to dial resource plugin with socketPath %s: %v", endpoint, err)
	}

	options, err := e.GetResourcePluginOptions(context.Background(), &pluginapi.Empty{})
	if err != nil {
		return fmt.Errorf("[ORM] failed to get resource plugin options: %v", err)
	}

	m.registerEndpoint(pluginName, options, e)

	return nil
}

// DeRegisterPlugin deregisters the plugin
func (m *ManagerImpl) DeRegisterPlugin(pluginName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if eI, ok := m.endpoints[pluginName]; ok {
		eI.E.Stop()
	}
}

func (m *ManagerImpl) registerEndpoint(resourceName string, options *pluginapi.ResourcePluginOptions, e endpoint2.Endpoint) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	old, ok := m.endpoints[resourceName]

	if ok && !old.E.IsStopped() {
		klog.V(2).Infof("[ORM] stop old endpoint: %v", old.E)
		old.E.Stop()
	}

	m.endpoints[resourceName] = endpoint2.EndpointInfo{E: e, Opts: options}
	klog.V(2).Infof("[ORM] Registered endpoint %v", e)
}

func (m *ManagerImpl) isVersionCompatibleWithPlugin(versions []string) bool {
	for _, version := range versions {
		for _, supportedVersion := range pluginapi.SupportedVersions {
			if version == supportedVersion {
				return true
			}
		}
	}
	return false
}

// Register registers a resource plugin.
func (m *ManagerImpl) Register(ctx context.Context, r *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	klog.Infof("[ORM] Got registration request from resource plugin with resource name %q", r.ResourceName)
	var versionCompatible bool
	for _, v := range pluginapi.SupportedVersions {
		if r.Version == v {
			versionCompatible = true
			break
		}
	}
	if !versionCompatible {
		errorString := fmt.Sprintf(errUnsupportedVersion, r.Version, pluginapi.SupportedVersions)
		klog.Infof("Bad registration request from resource plugin with resource name %q: %s", r.ResourceName, errorString)
		return &pluginapi.Empty{}, fmt.Errorf(errorString)
	}

	// TODO: for now, always accepts newest resource plugin. Later may consider to
	// add some policies here, e.g., verify whether an old resource plugin with the
	// same resource name is still alive to determine whether we want to accept
	// the new registration.
	success := make(chan bool)
	go m.addEndpoint(r, success)
	select {
	case pass := <-success:
		if pass {
			klog.Infof("[ORM] Register resource plugin for %s success", r.ResourceName)
			return &pluginapi.Empty{}, nil
		}
		klog.Errorf("[ORM] Register resource plugin for %s fail", r.ResourceName)
		return &pluginapi.Empty{}, fmt.Errorf("failed to register resource %s", r.ResourceName)
	case <-ctx.Done():
		klog.Errorf("[ORM] Register resource plugin for %s timeout", r.ResourceName)
		return &pluginapi.Empty{}, fmt.Errorf("timeout to register resource %s", r.ResourceName)
	}
}

func (m *ManagerImpl) addEndpoint(r *pluginapi.RegisterRequest, success chan<- bool) {
	new, err := endpoint2.NewEndpointImpl(filepath.Join(m.socketdir, r.Endpoint), r.ResourceName)
	if err != nil {
		klog.Errorf("[qosresourcemanager] Failed to dial resource plugin with request %v: %v", r, err)
		success <- false
		return
	}
	m.registerEndpoint(r.ResourceName, r.Options, new)
	success <- true
}
