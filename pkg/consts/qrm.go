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

package consts

const (
	// KubeletQoSResourceManagerCheckpoint is the name of the checkpoint file for kubelet QoS resource manager
	KubeletQoSResourceManagerCheckpoint = "kubelet_qrm_checkpoint"

	MainContainerNameAnnotationKey = "kubernetes.io/main-container-name"
)

const (
	// QRMResourceAnnotationKeyNUMABindResult is the annotation key for the numa binding result
	QRMResourceAnnotationKeyNUMABindResult = "qrm.katalyst.kubewharf.io/numa_bind_result"
)
