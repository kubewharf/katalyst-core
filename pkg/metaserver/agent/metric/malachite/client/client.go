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

package client

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

const (
	malachiteServicePort = 9002

	CgroupResource     = "cgroup/groups"
	CgroupPathParamKey = "cgroup_user_path"

	SystemIOResource      = "system/io"
	SystemNetResource     = "system/network"
	SystemMemoryResource  = "system/memory"
	SystemComputeResource = "system/compute"
)

type SystemResourceKind int

const (
	Compute SystemResourceKind = iota
	Memory
	IO
	Net
)

type MalachiteClient struct {
	// those fields are for testing
	sync.RWMutex
	urls             map[string]string
	relativePathFunc *func(podUID, containerId string) (string, error)

	fetcher pod.PodFetcher
}

func NewMalachiteClient(fetcher pod.PodFetcher) *MalachiteClient {
	urls := make(map[string]string)
	for _, path := range []string{
		CgroupResource,
		SystemIOResource,
		SystemNetResource,
		SystemComputeResource,
		SystemMemoryResource,
	} {
		urls[path] = fmt.Sprintf("http://localhost:%d/api/v1/%s", malachiteServicePort, path)
	}

	return &MalachiteClient{
		fetcher: fetcher,
		urls:    urls,
	}
}

// SetURL is used to implement UT for
func (c *MalachiteClient) SetURL(urls map[string]string) {
	c.Lock()
	defer c.Unlock()
	c.urls = urls
}
