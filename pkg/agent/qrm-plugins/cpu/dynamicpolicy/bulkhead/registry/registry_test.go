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

package registry

import (
	"reflect"
	"testing"
)

func TestNewDefaultPluginsPreservesOrder(t *testing.T) {
	t.Parallel()

	plugins, err := NewDefaultPlugins(nil)
	if err != nil {
		t.Fatalf("NewDefaultPlugins failed: %v", err)
	}
	got := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		got = append(got, plugin.Name())
	}
	want := []string{"cpuset_topology", "workqueue", "system_service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plugin order, got %v want %v", got, want)
	}
}
