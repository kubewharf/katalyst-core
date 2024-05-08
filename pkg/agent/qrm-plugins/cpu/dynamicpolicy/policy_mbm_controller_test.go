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

package dynamicpolicy

import (
	"context"
	"testing"
	"time"
)

func TestMBMController_Run_Stop(t *testing.T) {
	t.Parallel()
	mc := NewMBMController(80, time.Second*2)

	controller, ok := mc.(*mbmController)
	if !ok {
		t.Fatalf("unexpected type created")
	}

	mc.Run(context.TODO())
	if controller.mbmControllerCancel == nil {
		t.Errorf("expected cancel func after started; got nil")
	}

	mc.Stop()
	if controller.mbmControllerCancel != nil {
		t.Errorf("expected cancel func cleared after stop; got non-nil")
	}
}
