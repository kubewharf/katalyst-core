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

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

func (c *MalachiteClient) GetSysFreqData() (*types.SysFreqData, error) {
	payload, err := c.getRealtimePayload(RealtimeSysFreqResource)
	if err != nil {
		return nil, err
	}

	rsp := &types.MalachiteSysFreqResponse{}
	if err := json.Unmarshal(payload, rsp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal raw data, err %s", err)
	}

	if rsp.Status != 0 {
		return nil, fmt.Errorf("realtime sysfreq status is not ok, %d", rsp.Status)
	}

	c.checkSystemStatsOutOfDate("sysfreq", RealtimeUpdateTimeout, rsp.Data.SysFreq.UpdateTime)

	return &rsp.Data, nil
}
