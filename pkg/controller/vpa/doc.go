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

// Package vpa is the package that contains several separated controllers for vpa,
// and those different controllers should work together (in asynchronous way) to
// provide an integrated functionality. In production environment, it's ok
// to deploy those controllers as separated processes/containers/pods.
package vpa // import "github.com/kubewharf/katalyst-core/pkg/controller/vpa"
