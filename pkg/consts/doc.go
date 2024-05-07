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

// Package consts is the package that defines those universal const vars
// if const vars are not used specifically by a certain file, ie. may be used
// by other components or imported by other projects, then they should be defined here.
package consts // import "github.com/kubewharf/katalyst-core/pkg/consts"

type (
	PodContainerName string
	ContainerName    string
)
