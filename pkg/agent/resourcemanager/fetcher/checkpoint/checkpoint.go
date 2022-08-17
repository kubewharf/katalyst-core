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

package checkpoint

import (
	"encoding/json"

	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager/checksum"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
)

type ReporterManagerCheckpoint interface {
	checkpointmanager.Checkpoint
	GetData() map[string]*v1alpha1.GetReportContentResponse
}

// Data holds checkpoint data and its checksum
type Data struct {
	Data     map[string]*v1alpha1.GetReportContentResponse
	Checksum checksum.Checksum
}

// New returns an instance of Checkpoint
func New(reportResponses map[string]*v1alpha1.GetReportContentResponse) ReporterManagerCheckpoint {
	return &Data{
		Data: reportResponses,
	}
}

func (d *Data) MarshalCheckpoint() ([]byte, error) {
	d.Checksum = checksum.New(d.Data)
	return json.Marshal(*d)
}

func (d *Data) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, d)
}

func (d *Data) VerifyChecksum() error {
	return d.Checksum.Verify(d.Data)
}

func (d *Data) GetData() map[string]*v1alpha1.GetReportContentResponse {
	return d.Data
}
