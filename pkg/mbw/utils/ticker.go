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

package utils

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// TickUntilDone runs a given action at a tick rate specified by refreshRate, it returns if the context is canceled
func TickUntilDone(ctx context.Context, refreshRate uint64, action func() error) (err error) {
	return wait.PollImmediateUntil(time.Duration(refreshRate)*time.Millisecond,
		func() (bool, error) { return false, action() },
		ctx.Done(),
	)
}