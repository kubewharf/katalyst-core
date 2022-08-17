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

package malachite

import (
	"fmt"
	"io/ioutil"
	"net/http"
)

const (
	malachiteServicePort = 9002
	cgroupResource       = "cgroup/groups"
	cgroupPathParamKey   = "cgroup_user_path"
	computeResource      = "system/compute"
	memoryResource       = "system/memory"
	ioResource           = "system/io"
	netResource          = "system/network"
)

type SystemResourceKind int

const (
	Compute SystemResourceKind = iota
	Memory
	IO
	Net
)

var DefaultClient = New()

type client struct {
	urls map[string]string
}

type MalachiteClient interface {
	GetCgroupStats(cgroup string) ([]byte, error)
	GetSystemStats(kind SystemResourceKind) ([]byte, error)
}

func New() MalachiteClient {
	urls := make(map[string]string)
	for _, path := range []string{cgroupResource, computeResource, memoryResource, ioResource, netResource} {
		urls[path] = fmt.Sprintf("http://localhost:%d/api/v1/%s", malachiteServicePort, path)
	}
	return &client{
		urls: urls,
	}
}

// SetURL is used to implement UT for
func (c *client) SetURL(urls map[string]string) {
	c.urls = urls
}

func (c *client) GetCgroupStats(cgroupPath string) ([]byte, error) {
	url, ok := c.urls[cgroupResource]
	if !ok {
		return nil, fmt.Errorf("no url for %v", cgroupResource)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to http.NewRequest, url: %s, err %s", url, err)
	}

	q := req.URL.Query()
	q.Add(cgroupPathParamKey, cgroupPath)
	req.URL.RawQuery = q.Encode()

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to http.DefaultClient.Do, url: %s, err %s", req.URL, err)
	}

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, url: %s", rsp.StatusCode, req.URL)
	}

	defer rsp.Body.Close()
	return ioutil.ReadAll(rsp.Body)
}

func (c *client) GetSystemStats(kind SystemResourceKind) ([]byte, error) {
	resource := ""
	switch kind {
	case Compute:
		resource = computeResource
	case Memory:
		resource = memoryResource
	case IO:
		resource = ioResource
	case Net:
		resource = netResource
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

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, url: %s", rsp.StatusCode, req.URL)
	}

	defer rsp.Body.Close()
	return ioutil.ReadAll(rsp.Body)
}
