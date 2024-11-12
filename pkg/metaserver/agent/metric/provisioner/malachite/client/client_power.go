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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

func (c *MalachiteClient) GetPowerData() (*types.PowerData, error) {
	payload, err := c.getRealtimePowerPayload(RealtimePowerResource)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachitePowerResponse{}
	if err := json.Unmarshal(payload, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system compute stats raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("system compute stats status is not ok, %d", rsp.Status)
	}

	return &rsp.Data, nil
}

func (c *MalachiteClient) getRealtimePowerPayload(resource string) ([]byte, error) {
	c.RLock()
	defer c.RUnlock()

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
