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
	"io/ioutil"
	"net/http"
	"sync"
)

const (
	malachiteServicePort = 9002

	CgroupResource     = "cgroup/groups"
	CgroupPathParamKey = "cgroup_user_path"

	SystemComputeResource = "system/compute"
	SystemMemoryResource  = "system/memory"
	SystemIOResource      = "system/io"
	SystemNetResource     = "system/network"
)

type SystemResourceKind int

const (
	Compute SystemResourceKind = iota
	Memory
	IO
	Net
)

type Client struct {
	sync.RWMutex
	urls map[string]string
}

type MalachiteClient interface {
	GetCgroupStats(cgroup string) ([]byte, error)
	GetSystemStats(kind SystemResourceKind) ([]byte, error)
}

func New() MalachiteClient {
	urls := make(map[string]string)
	for _, path := range []string{CgroupResource, SystemComputeResource, SystemMemoryResource, SystemIOResource, SystemNetResource} {
		urls[path] = fmt.Sprintf("http://localhost:%d/api/v1/%s", malachiteServicePort, path)
	}
	return &Client{
		urls: urls,
	}
}

// SetURL is used to implement UT for
func (c *Client) SetURL(urls map[string]string) {
	c.Lock()
	defer c.Unlock()

	c.urls = urls
}

func (c *Client) GetCgroupStats(cgroupPath string) ([]byte, error) {
	c.RLock()
	defer c.RUnlock()

	url, ok := c.urls[CgroupResource]
	if !ok {
		return nil, fmt.Errorf("no url for %v", CgroupResource)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to http.NewRequest, url: %s, err %s", url, err)
	}

	q := req.URL.Query()
	q.Add(CgroupPathParamKey, cgroupPath)
	req.URL.RawQuery = q.Encode()

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to http.DefaultClient.Do, url: %s, err %s", req.URL, err)
	}

	defer func() { _ = rsp.Body.Close() }()

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, url: %s", rsp.StatusCode, req.URL)
	}

	return ioutil.ReadAll(rsp.Body)
}

func (c *Client) GetSystemStats(kind SystemResourceKind) ([]byte, error) {
	c.RLock()
	defer c.RUnlock()

	resource := ""
	switch kind {
	case Compute:
		resource = SystemComputeResource
	case Memory:
		resource = SystemMemoryResource
	case IO:
		resource = SystemIOResource
	case Net:
		resource = SystemNetResource
	default:
		return nil, fmt.Errorf("unknown system resource kind, %v", kind)
	}

	url, ok := c.urls[resource]
	if !ok {
		return nil, fmt.Errorf("no url for %v", resource)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to http.NewRequest, url: %s, err %s", url, err)
	}

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to http.DefaultClient.Do, url: %s, err %s", req.URL, err)
	}

	defer func() { _ = rsp.Body.Close() }()

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, url: %s", rsp.StatusCode, req.URL)
	}

	return ioutil.ReadAll(rsp.Body)
}
