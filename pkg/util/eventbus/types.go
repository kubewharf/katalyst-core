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

package eventbus

import (
	"time"
)

type BaseEvent interface {
	getTime() time.Time
}

type BaseEventImpl struct {
	Time time.Time
}

func (b *BaseEventImpl) getTime() time.Time {
	return b.Time
}

type RawCGroupEvent struct {
	BaseEventImpl
	Cost       time.Duration
	CGroupPath string
	CGroupFile string
	Data       string
	OldData    string
}

type RawProcfsEvent struct {
	BaseEventImpl
	Cost     time.Duration
	ProcPath string
	ProcFile string
	Data     string
	OldData  string
}

type SyscallEvent struct {
	BaseEventImpl
	Cost        time.Duration
	Syscall     string
	PodUID      string
	ContainerID string
	Logs        []SyscallLog
}

type SyscallLog struct {
	Time     time.Time
	KeyValue map[string]string
}
