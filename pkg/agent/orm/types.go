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

package orm

const (
	MetricAddPodTimeout    = "ORM_add_pod_timeout"
	MetricDeletePodTImeout = "ORM_delete_pod_timeout"

	MainContainerNameAnnotationKey = "kubernetes.io/main-container-name"

	KubeletPluginsDirSELinuxLabel = "system_u:object_r:container_file_t:s0"
)

const (
	errBadSocket = "bad socketPath, must be an absolute path:"

	// errUnsupportedVersion is the error raised when the resource plugin uses an API version not
	// supported by the ORM registry
	errUnsupportedVersion = "requested API version %q is not supported by ORM. Supported version is %q"

	// errListenSocket is the error raised when the registry could not listen on the socket
	errListenSocket = "failed to listen to socket while starting resource plugin registry, with error"
)
