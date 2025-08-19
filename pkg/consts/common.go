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

import (
	"math"
)

const (
	ControlKnobON  = "true"
	ControlKnobOFF = "false"
)

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

	EventReasonContainerStopped = "ContainerStopped"
)

// const variable for pod eviction action identifier in event.
const (
	EventActionEvicting          = "Evicting"
	EventActionContainerStopping = "ContainerStopping"
)

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
	KatalystComponentScheduler  KatalystComponent = "scheduler"
)

// common fields for ordinary k8s objects.
const (
	ObjectFieldNameSpec   = "spec"
	ObjectFieldNameStatus = "status"
)

// common disk types.
const (
	DiskTypeUnknown = 0
	DiskTypeHDD     = 1
	DiskTypeSSD     = 2
	DiskTypeNVME    = 3
	DiskTypeVIRTIO  = 4
)

var (
	EXP1  = 1.0 / math.Exp(5.0/60.0)
	EXP5  = 1.0 / math.Exp(5.0/300.0)
	EXP15 = 1.0 / math.Exp(5.0/900.0)
)

// event bus topics
const (
	TopicNameApplyCGroup = "ApplyCGroup"
	TopicNameApplyProcFS = "ApplyProcFS"
	TopicNameApplySysFS  = "ApplySysFS"
	TopicNameSyscall     = "Syscall"
)

const (
	SystemNodeDir        = "/sys/devices/system/node/"
	SystemCpuDir         = "/sys/devices/system/cpu/"
	SystemL3CacheSubPath = "cache/index3/id"
)

const (
	PlatformGeona   = "geona"
	PlatformMilan   = "milan"
	PlatformRome    = "rome"
	PlatformRapids  = "intel_rapids"
	PlatformLake    = "intel_lake"
	PlatformUnknown = "unknown"
)

const (
	AMDRomeArch     = "Zen2"
	AMDMilanArch    = "Zen3"
	AMDGenoaArch    = "Zen4"
	IntelRapidsArch = "Rapids"
	IntelLakeArch   = "Lake"
)

const (
	BytesPerGB = 1e9
	MaxMBMDiff = 50 * BytesPerGB
	MaxMBMStep = 3 * BytesPerGB
	MaxMBGBps  = 400 * BytesPerGB // 400 GB/s is the maximum bandwidth of L3 cache in Milan, Genoa, and Rapids platforms
)
