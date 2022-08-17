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
	// OwnerReferenceIndex is the lookup name for the index function
	OwnerReferenceIndex = "owner-reference-index"
	// TargetReferenceIndex is the lookup name for the index function
	TargetReferenceIndex = "target-reference-index"
	// VPANameIndex is the lookup name for the index function
	VPANameIndex = "vpa-name-index"
)

// const variables for pod eviction reason identifier in event.
const (
	EventReasonEvictFailed              = "EvictFailed"
	EventReasonEvictCreated             = "EvictCreated"
	EventReasonEvictExceededGracePeriod = "EvictExceededGracePeriod"
	EventReasonEvictSucceeded           = "EvictSucceeded"
)

// EventActionEvicting is const variable for pod eviction action identifier in event.
const EventActionEvicting = "Evicting"

// KeySeparator : to split parts of a key
const KeySeparator = "/"

// KatalystNodeDomainPrefix domain prefix for taint, label, annotation keys.
const KatalystNodeDomainPrefix = "node.katalyst.kubewharf.io"

// KatalystComponent defines the component name that current process is running as.
type KatalystComponent string

const (
	KatalystComponentAgent      KatalystComponent = "agent"
	KatalystComponentController KatalystComponent = "controller"
	KatalystComponentWebhook    KatalystComponent = "webhook"
	KatalystComponentMetric     KatalystComponent = "metric"
)

// common fields for ordinary k8s objects.
const (
	ObjectFieldNameSpec   = "spec"
	ObjectFieldNameStatus = "status"
)
