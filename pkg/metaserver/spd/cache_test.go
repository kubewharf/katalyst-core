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

package spd

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

func newTestCache(t *testing.T) (*Cache, func()) {
	dir, err := ioutil.TempDir("", "spd-cache-test")
	require.NoError(t, err)

	cpm, err := checkpointmanager.NewCheckpointManager(dir)
	require.NoError(t, err)

	// expiredTime is short so clearUnusedSPDs treats every non-pinned entry as expired
	c, err := NewSPDCache(cpm, true, 10*time.Millisecond, 1*time.Millisecond, 3, 0.5)
	require.NoError(t, err)

	cleanup := func() { os.RemoveAll(dir) }
	return c, cleanup
}

func TestCache_PinPreventsEviction(t *testing.T) {
	t.Parallel()

	c, cleanup := newTestCache(t)
	defer cleanup()

	pinnedSPD := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{Name: "pinned", Namespace: "default"},
	}
	normalSPD := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{Name: "normal", Namespace: "default"},
	}

	pinnedKey := "default/pinned"
	normalKey := "default/normal"

	require.NoError(t, c.SetSPD(pinnedKey, pinnedSPD))
	require.NoError(t, c.SetSPD(normalKey, normalSPD))

	c.SetSPDPinned(pinnedKey, true)
	require.True(t, c.IsSPDPinned(pinnedKey))
	require.False(t, c.IsSPDPinned(normalKey))

	// wait long enough so non-pinned entries are considered expired by lastGetTime
	time.Sleep(20 * time.Millisecond)
	c.clearUnusedSPDs(context.TODO())

	got, _ := c.GetSPD(pinnedKey, false)
	require.NotNil(t, got, "pinned spd must remain in cache")

	got, _ = c.GetSPD(normalKey, false)
	require.Nil(t, got, "normal spd should be evicted")
}

func TestCache_UnpinAllowsEviction(t *testing.T) {
	t.Parallel()

	c, cleanup := newTestCache(t)
	defer cleanup()

	spd := &workloadapis.ServiceProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{Name: "spd", Namespace: "default"},
	}
	key := "default/spd"
	require.NoError(t, c.SetSPD(key, spd))

	c.SetSPDPinned(key, true)
	time.Sleep(20 * time.Millisecond)
	c.clearUnusedSPDs(context.TODO())
	got, _ := c.GetSPD(key, false)
	require.NotNil(t, got)

	// unpin and the next clearUnusedSPDs should evict it
	c.SetSPDPinned(key, false)
	require.False(t, c.IsSPDPinned(key))

	time.Sleep(20 * time.Millisecond)
	c.clearUnusedSPDs(context.TODO())
	got, _ = c.GetSPD(key, false)
	require.Nil(t, got, "spd should be evicted after unpinning")
}
