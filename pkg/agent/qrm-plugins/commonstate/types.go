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

package commonstate

import "github.com/kubewharf/katalyst-core/pkg/util/general"

// ControlKnobInfo shows common types of control knobs:
// 1. applied by cgroup manager according to entryName, subEntryName, cgroupIfaceName and cgroupSubsysName
// 2. applied by QRM framework according to ociPropertyName
//
// there may be new types of control knobs,
// we won't modified this struct to identify them,
// and we will register custom per-control-knob executor to deal with them.
type ControlKnobInfo struct {
	ControlKnobValue string `json:"control_knob_value"`

	// for control knobs applied by cgroup manager
	// according to entryName, subEntryName, cgroupIfaceName and cgroupSubsysName
	CgroupVersionToIfaceName map[string]string `json:"cgroup_version_to_iface_name"`
	CgroupSubsysName         string            `json:"cgroup_subsys_name"`

	// for control knobs applied by QRM framework according to ociPropertyName
	OciPropertyName string `json:"oci_property_name"`
}

func (cki ControlKnobInfo) Clone() ControlKnobInfo {
	return ControlKnobInfo{
		ControlKnobValue:         cki.ControlKnobValue,
		CgroupVersionToIfaceName: general.DeepCopyMap(cki.CgroupVersionToIfaceName),
		CgroupSubsysName:         cki.CgroupSubsysName,
		OciPropertyName:          cki.OciPropertyName,
	}
}

type ExtraControlKnobConfigs map[string]ExtraControlKnobConfig

type ExtraControlKnobConfig struct {
	PodExplicitlyAnnotationKey string            `json:"pod_explicitly_annotation_key"`
	QoSLevelToDefaultValue     map[string]string `json:"qos_level_to_default_value"`
	ControlKnobInfo            `json:"control_knob_info"`
}
