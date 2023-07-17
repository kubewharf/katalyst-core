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

package checkpoint

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

// TestWriteLoadDeleteSPDs validates all combinations of write, load, and delete
func TestWriteLoadDeleteSPDs(t *testing.T) {
	t.Parallel()

	testSPDs := []*v1alpha1.ServiceProfileDescriptor{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "default",
				Name:              "spd-1",
				CreationTimestamp: metav1.Unix(metav1.Now().Unix(), 0),
			},
			Spec: v1alpha1.ServiceProfileDescriptorSpec{
				BusinessIndicator: []v1alpha1.ServiceBusinessIndicatorSpec{
					{
						Name: v1alpha1.ServiceBusinessIndicatorNameRPCLatency,
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "default",
				Name:              "spd-2",
				DeletionTimestamp: &metav1.Time{Time: metav1.Unix(metav1.Now().Unix(), 0).Time},
			},
			Status: v1alpha1.ServiceProfileDescriptorStatus{
				BusinessStatus: []v1alpha1.ServiceBusinessIndicatorStatus{
					{
						Name: v1alpha1.ServiceBusinessIndicatorNameRPCLatency,
					},
				},
			},
		},
	}

	dir, err := ioutil.TempDir("", "checkpoint-TestWriteLoadDeleteSPDs")
	if err != nil {
		t.Errorf("failed to allocate temp directory for TestWriteLoadDeleteSPDs error=%v", err)
	}
	defer os.RemoveAll(dir)

	cpm, err := checkpointmanager.NewCheckpointManager(dir)
	if err != nil {
		t.Errorf("failed to initialize checkpoint manager error=%v", err)
	}

	for _, p := range testSPDs {
		if err := WriteSPD(cpm, p); err != nil {
			t.Errorf("failed to Write SPD: %v", err)
		}
	}

	// verify the correct written files are loaded from disk
	spdList, err := LoadSPDs(cpm)
	if err != nil {
		t.Errorf("failed to Load spds: %v", err)
	}

	// loop through contents and check make sure
	// what was loaded matched the expected results.
	for _, p := range testSPDs {
		spdName := p.GetName()
		var spd *v1alpha1.ServiceProfileDescriptor
		for _, check := range spdList {
			if check.GetName() == spdName {
				spd = check
				break
			}
		}

		if spd != nil {
			if !reflect.DeepEqual(p, spd) {
				t.Errorf("expected %#v, \ngot %#v", p, spd)
			}
		} else {
			t.Errorf("got unexpected result for %v, should have been loaded", spdName)
		}

		err = DeleteSPD(cpm, p)
		if err != nil {
			t.Errorf("failed to delete spd %v", spdName)
		}
	}
	// finally validate the contents of the directory is empty.
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Errorf("failed to read directory %v", dir)
	}
	if len(files) > 0 {
		t.Errorf("directory %v should be empty but found %#v", dir, files)
	}
}
