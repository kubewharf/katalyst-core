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

// Package model holds the leaf value types shared by the topology parent
// package and its deadlock/phase subpackages. It deliberately depends only on
// the standard library and the machine package so that it cannot import the
// parent topology package, topology/deadlock, or topology/phase. This breaks
// the previous import cycle: deadlock and phase depend on model, never back on
// the parent package.
package model

// DomainID identifies an ownership domain without relying on cgroup names.
type DomainID string

const (
	DomainPrimary DomainID = "primary"
	DomainReclaim DomainID = "reclaim"
)
