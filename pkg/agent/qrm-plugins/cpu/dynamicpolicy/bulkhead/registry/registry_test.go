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
	want := []string{"cpuset_topology", "workqueue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plugin order, got %v want %v", got, want)
	}
}
