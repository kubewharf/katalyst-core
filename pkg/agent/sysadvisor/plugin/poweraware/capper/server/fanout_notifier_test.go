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

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_fanoutNotifier_IsEmpty(t *testing.T) {
	t.Parallel()

	notifier := newNotifier()
	assert.Truef(t, notifier.IsEmpty(), "initial notifier is empty")

	ctx := context.TODO()
	_ = notifier.Register(ctx)
	assert.Falsef(t, notifier.IsEmpty(), "has 1 registered, expecting not empty")

	notifier.Unregister(ctx)
	assert.Truef(t, notifier.IsEmpty(), "sub unregistered, expecting empty again")
}

func Test_fanoutNotifier_Register_Notify(t *testing.T) {
	t.Parallel()

	notifier := newNotifier()
	ctx := context.TODO()

	ch := notifier.Register(ctx)
	assert.Equal(t, 0, len(ch), "expecting empty buffer chan")

	notifier.Notify()

	assert.Equal(t, 1, len(ch), "expecting 1-event stuffed chan")
}
