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

package compatibility

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type KubernetesVersionCandidate struct {
	Resource               string
	DiscoveryGroupVersions []string
	SupportedVersions      []string
}

type KubernetesVersionConfig struct {
	APIName            string
	CheckpointFileName string
	Candidates         []KubernetesVersionCandidate
}

type kubernetesVersionCheckpoint struct {
	VersionCheckpoint
	fileName string
}

func (c *kubernetesVersionCheckpoint) FileName() string {
	return c.fileName
}

type kubernetesVersionResolver struct {
	discoveryClient discovery.DiscoveryInterface
	versionStore    VersionStore
	config          KubernetesVersionConfig
}

func NewKubernetesVersionResolver(
	discoveryClient discovery.DiscoveryInterface,
	versionStore VersionStore,
	config KubernetesVersionConfig,
) VersionResolver {
	return &kubernetesVersionResolver{
		discoveryClient: discoveryClient,
		versionStore:    versionStore,
		config:          config,
	}
}

func (r *kubernetesVersionResolver) Get(_ context.Context) (string, error) {
	if r.versionStore != nil {
		checkpoint := r.newCheckpoint()
		found, err := r.versionStore.Get(checkpoint)
		if err != nil {
			klog.ErrorS(err, "Read Kubernetes API compatibility checkpoint", "api", r.config.APIName)
		} else if found {
			if r.isSupported(checkpoint.Version()) {
				return checkpoint.Version(), nil
			}
			klog.ErrorS(nil, "Ignore unsupported cached Kubernetes API version",
				"api", r.config.APIName, "version", checkpoint.Version())
		}
	}

	return r.discover()
}

func (r *kubernetesVersionResolver) Refresh(_ context.Context) (string, error) {
	if r.versionStore != nil {
		if err := r.versionStore.Delete(r.newCheckpoint()); err != nil {
			klog.ErrorS(err, "Delete stale Kubernetes API compatibility checkpoint", "api", r.config.APIName)
		}
	}
	return r.discover()
}

func (r *kubernetesVersionResolver) discover() (string, error) {
	if len(r.config.Candidates) == 0 {
		return "", fmt.Errorf("%s has no Kubernetes API candidates", r.config.APIName)
	}

	var lastNotFoundErr error
	for _, candidate := range r.config.Candidates {
		discoveredGroupVersion, resource, err := native.FindSupportedAPIResource(
			r.discoveryClient,
			candidate.Resource,
			candidate.DiscoveryGroupVersions...,
		)
		if err != nil {
			if native.IsAPIResourceNotFound(err) {
				lastNotFoundErr = err
				continue
			}
			return "", fmt.Errorf("discover %s: %w", r.config.APIName, err)
		}

		version := discoveredGroupVersion
		if resource.Version != "" {
			version = schema.GroupVersion{Group: resource.Group, Version: resource.Version}.String()
		}
		if !candidateSupportsVersion(candidate, version) {
			return "", fmt.Errorf(
				"%s advertises unsupported request group/version %q", candidate.Resource, version)
		}

		r.persist(version)
		return version, nil
	}

	return "", fmt.Errorf("discover %s: %w", r.config.APIName, lastNotFoundErr)
}

func (r *kubernetesVersionResolver) persist(version string) {
	if r.versionStore == nil {
		return
	}
	checkpoint := r.newCheckpoint()
	checkpoint.SetVersion(version)
	if err := r.versionStore.Put(checkpoint); err != nil {
		klog.ErrorS(err, "Persist Kubernetes API compatibility checkpoint", "api", r.config.APIName)
	}
}

func (r *kubernetesVersionResolver) newCheckpoint() *kubernetesVersionCheckpoint {
	return &kubernetesVersionCheckpoint{
		VersionCheckpoint: NewVersionCheckpoint(""),
		fileName:          r.config.CheckpointFileName,
	}
}

func (r *kubernetesVersionResolver) isSupported(version string) bool {
	for _, candidate := range r.config.Candidates {
		if candidateSupportsVersion(candidate, version) {
			return true
		}
	}
	return false
}

func candidateSupportsVersion(candidate KubernetesVersionCandidate, version string) bool {
	for _, supportedVersion := range candidate.SupportedVersions {
		if version == supportedVersion {
			return true
		}
	}
	return false
}
