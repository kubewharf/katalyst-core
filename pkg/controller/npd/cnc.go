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

package npd

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const npdConfigHashLength = 12

var npdGVR = metav1.GroupVersionResource(v1alpha1.SchemeGroupVersion.WithResource(v1alpha1.ResourceNameKatalystNPD))

func (nc *NPDController) applyNPDTargetConfigToCNC(npd *v1alpha1.NodeProfileDescriptor) error {
	// get cnc
	cnc, err := nc.cncLister.Get(npd.Name)
	if err != nil {
		klog.Errorf("[npd] get cnc %s error: %v", npd.Name, err)
		return err
	}
	oldCNC := cnc.DeepCopy()

	hash, err := CalculateNPDHash(npd)
	if err != nil {
		klog.Errorf("[npd] calculate npd hash of %s error: %v", npd.Name, err)
		return err
	}

	found := false
	for i, tc := range cnc.Status.KatalystCustomConfigList {
		if tc.ConfigType != npdGVR || tc.ConfigName != npd.Name {
			continue
		}
		if cnc.Status.KatalystCustomConfigList[i].Hash == hash {
			klog.V(4).Infof("[npd] cnc %s hash %s not need to update", cnc.GetName(), hash)
			return nil
		}
		klog.V(4).Infof("[npd] update cnc %s hash %s -> %s", cnc.GetName(), cnc.Status.KatalystCustomConfigList[i].Hash, hash)
		cnc.Status.KatalystCustomConfigList[i].Hash = hash
		found = true
		break
	}
	if !found {
		tc := configapis.TargetConfig{
			ConfigType: npdGVR,
			ConfigName: npd.Name,
			Hash:       hash,
		}
		cnc.Status.KatalystCustomConfigList = append(cnc.Status.KatalystCustomConfigList, tc)
		klog.V(4).Infof("[npd] add cnc %s hash %s", cnc.GetName(), hash)
	}

	// update cnc
	_, err = nc.cncControl.PatchCNCStatus(nc.ctx, cnc.Name, oldCNC, cnc)
	return err
}

// CalculateNPDHash calculate current npd hash by its status
func CalculateNPDHash(npd *v1alpha1.NodeProfileDescriptor) (string, error) {
	if npd == nil {
		return "", fmt.Errorf("npd is nil")
	}

	npdCopy := &v1alpha1.NodeProfileDescriptor{}
	npdCopy.Status = npd.Status
	data, err := json.Marshal(npdCopy)
	if err != nil {
		return "", err
	}

	return general.GenerateHash(data, npdConfigHashLength), nil
}
