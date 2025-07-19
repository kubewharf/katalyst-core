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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// Global mocks to avoid conflicts in parallel tests
var (
	globalContainerIDMocker *mockey.Mocker
	globalPathMocker        *mockey.Mocker
	globalCPUMocker         *mockey.Mocker
	globalApplyMocker       *mockey.Mocker
	globalPodMocker         *mockey.Mocker
	globalPodPathMocker     *mockey.Mocker
)

func init() {
	// Set up global mocks once for all tests
	globalContainerIDMocker = mockey.Mock(native.GetContainerID).To(func(pod *v1.Pod, containerName string) (string, error) {
		switch containerName {
		case "container1":
			return "container1-id", nil
		case "container2":
			return "container2-id", nil
		case "quota-test-container1-id":
			return "quota-test-container1-id", nil
		case "quota-test-container2-id":
			return "quota-test-container2-id", nil
		default:
			return "", fmt.Errorf("unknown container: %s", containerName)
		}
	}).Build()

	globalPathMocker = mockey.Mock(common.GetContainerRelativeCgroupPath).To(func(podUID, containerID string) (string, error) {
		switch containerID {
		case "container1-id":
			return "pod/test-pod-uid/container1-id", nil
		case "container2-id":
			return "pod/test-pod-uid/container2-id", nil
		case "quota-test-container1-id":
			return "pod/test-pod-uid/quota-test-container1-id", nil
		case "quota-test-container2-id":
			return "pod/test-pod-uid/quota-test-container2-id", nil
		default:
			return "", fmt.Errorf("unknown container ID: %s", containerID)
		}
	}).Build()

	globalCPUMocker = mockey.Mock(cgroupmgr.GetCPUWithRelativePath).Return(&common.CPUStats{
		CpuQuota:  -1, // Indicates no quota set
		CpuPeriod: 100000,
	}, nil).Build()

	globalApplyMocker = mockey.Mock(cgroupmgr.ApplyCPUWithRelativePath).Return(nil).Build()

	globalPodMocker = mockey.Mock((*pod.PodFetcherStub).GetPodList).Return([]*v1.Pod{}, nil).Build()

	globalPodPathMocker = mockey.Mock(common.GetPodAbsCgroupPath).Return("/sys/fs/cgroup/cpu/pod/test-pod-uid", nil).Build()
}

// Mock implementations for testing
type mockDirEntry struct {
	name  string
	isDir bool
}

func (m *mockDirEntry) Name() string               { return m.name }
func (m *mockDirEntry) IsDir() bool                { return m.isDir }
func (m *mockDirEntry) Type() fs.FileMode          { return 0 }
func (m *mockDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestDynamicPolicy_getAllPodsPathMap(t *testing.T) {
	t.Parallel()

	// Initialize test object
	policy := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	t.Run("When no pods exist", func(t *testing.T) {
		t.Parallel()

		// Test with empty pod list
		resultMap, err := policy.getAllPodsPathMap()

		// Should not panic and return empty map
		assert.NoError(t, err)
		assert.NotNil(t, resultMap)
		assert.Len(t, resultMap, 0)
	})

	t.Run("When at least one pod exists", func(t *testing.T) {
		t.Parallel()

		// Mock Pod data
		mockPods := []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					UID:       types.UID("test-pod-uid"),
				},
				Spec: v1.PodSpec{NodeName: "node-1"},
			},
		}

		// Update the global mock to return our test pods
		globalPodMocker.To(func(ctx context.Context, podFilter func(*v1.Pod) bool) ([]*v1.Pod, error) {
			return mockPods, nil
		})

		// Test with at least one pod
		resultMap, err := policy.getAllPodsPathMap()

		// Should handle gracefully
		assert.NoError(t, err)
		assert.NotNil(t, resultMap)

		// Verify that the function processed the pod and added it to the result map
		assert.Len(t, resultMap, 1, "Result map should contain exactly one pod")

		// Verify the pod in the result map
		podPath := "/sys/fs/cgroup/cpu/pod/test-pod-uid"
		pod, exists := resultMap[podPath]
		assert.True(t, exists, "Pod should exist in result map with the expected path")
		assert.NotNil(t, pod, "Pod should not be nil")
		assert.Equal(t, "test-pod", pod.Name, "Pod name should match")
		assert.Equal(t, "default", pod.Namespace, "Pod namespace should match")
		assert.Equal(t, types.UID("test-pod-uid"), pod.UID, "Pod UID should match")

		// Verify that the mocks were called
		assert.True(t, globalPodMocker.Times() > 0, "GetPodList should have been called")
		assert.True(t, globalPodPathMocker.Times() > 0, "GetPodAbsCgroupPath should have been called")
	})
}

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	t.Parallel()

	// Initialize test object
	policy := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	t.Run("When path does not exist", func(t *testing.T) {
		t.Parallel()

		// Test with non-existent path
		result, err := policy.getAllDirs("/non/existent/path")

		// Should handle error gracefully
		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("When path exists but is empty", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory for testing
		tempDir, err := os.MkdirTemp("", "test-dir")
		assert.NoError(t, err)
		defer os.RemoveAll(tempDir)

		result, err := policy.getAllDirs(tempDir)

		// Should return empty slice
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result, 0)
	})

	t.Run("When path contains mixed files and directories", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory with mixed content
		tempDir, err := os.MkdirTemp("", "test-dir")
		assert.NoError(t, err)
		defer os.RemoveAll(tempDir)

		// Create subdirectories
		subDir1 := filepath.Join(tempDir, "dir1")
		subDir2 := filepath.Join(tempDir, "dir2")
		subDir3 := filepath.Join(tempDir, "dir3")
		err = os.Mkdir(subDir1, 0o755)
		assert.NoError(t, err)
		err = os.Mkdir(subDir2, 0o755)
		assert.NoError(t, err)
		err = os.Mkdir(subDir3, 0o755)
		assert.NoError(t, err)

		// Create files
		file1 := filepath.Join(tempDir, "file1.txt")
		file2 := filepath.Join(tempDir, "file2.log")
		file3 := filepath.Join(tempDir, "config.yaml")
		err = os.WriteFile(file1, []byte("test"), 0o644)
		assert.NoError(t, err)
		err = os.WriteFile(file2, []byte("test"), 0o644)
		assert.NoError(t, err)
		err = os.WriteFile(file3, []byte("test"), 0o644)
		assert.NoError(t, err)

		result, err := policy.getAllDirs(tempDir)

		// Should return only directories
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result, 3, "Should return exactly 3 directories")

		// Verify all expected directories are present
		assert.Contains(t, result, "dir1", "Should contain dir1")
		assert.Contains(t, result, "dir2", "Should contain dir2")
		assert.Contains(t, result, "dir3", "Should contain dir3")

		// Verify no files are included
		assert.NotContains(t, result, "file1.txt", "Should not contain file1.txt")
		assert.NotContains(t, result, "file2.log", "Should not contain file2.log")
		assert.NotContains(t, result, "config.yaml", "Should not contain config.yaml")

		// Verify result is a slice of strings
		for _, dir := range result {
			assert.IsType(t, "", dir, "Each result should be a string")
			assert.NotEmpty(t, dir, "Directory name should not be empty")
		}

		// Verify all returned items are actually directories in the filesystem
		for _, dirName := range result {
			dirPath := filepath.Join(tempDir, dirName)
			fileInfo, err := os.Stat(dirPath)
			assert.NoError(t, err, "Should be able to stat directory %s", dirName)
			assert.True(t, fileInfo.IsDir(), "Item %s should be a directory", dirName)
		}
	})
}

func TestDynamicPolicy_getAllContainersRelativePathMap(t *testing.T) {
	t.Parallel()

	// Initialize test object
	policy := &DynamicPolicy{}

	t.Run("When pod has no containers", func(t *testing.T) {
		t.Parallel()

		// Create Pod without containers
		emptyPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "empty-pod",
				Namespace: "default",
				UID:       types.UID("empty-pod-uid"),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{},
			},
		}

		// Execute test method
		result := policy.getAllContainersRelativePathMap(emptyPod)

		// Should handle gracefully
		assert.NotNil(t, result, "Result should not be nil")
		assert.IsType(t, map[string]*v1.Container{}, result, "Result should be a map[string]*v1.Container")
		assert.Len(t, result, 0, "Should return empty map for pod with no containers")
	})

	t.Run("When pod has containers", func(t *testing.T) {
		t.Parallel()

		// Create test Pod with containers
		testPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				UID:       types.UID("test-pod-uid"),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container1",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("1"),
							},
							Limits: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
					{
						Name: "container2",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("0.5"),
							},
							Limits: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
		}

		// Test the real function
		resultMap := policy.getAllContainersRelativePathMap(testPod)

		// Verify the result
		assert.NotNil(t, resultMap, "Result map should not be nil")
		assert.Len(t, resultMap, 2, "Result map should contain exactly 2 containers")

		// Verify container1
		container1Path := "pod/test-pod-uid/container1-id"
		container1, exists := resultMap[container1Path]
		assert.True(t, exists, "Container1 should exist in result map")
		assert.NotNil(t, container1, "Container1 should not be nil")
		assert.Equal(t, "container1", container1.Name, "Container1 name should match")
		assert.Equal(t, resource.MustParse("1"), container1.Resources.Requests[v1.ResourceCPU], "Container1 CPU request should match")
		assert.Equal(t, resource.MustParse("2"), container1.Resources.Limits[v1.ResourceCPU], "Container1 CPU limit should match")

		// Verify container2
		container2Path := "pod/test-pod-uid/container2-id"
		container2, exists := resultMap[container2Path]
		assert.True(t, exists, "Container2 should exist in result map")
		assert.NotNil(t, container2, "Container2 should not be nil")
		assert.Equal(t, "container2", container2.Name, "Container2 name should match")
		assert.Equal(t, resource.MustParse("0.5"), container2.Resources.Requests[v1.ResourceCPU], "Container2 CPU request should match")
		assert.Equal(t, resource.MustParse("1"), container2.Resources.Limits[v1.ResourceCPU], "Container2 CPU limit should match")

		// Verify that the mocks were called
		assert.True(t, globalContainerIDMocker.Times() >= 2, "GetContainerID should be called for each container")
		assert.True(t, globalPathMocker.Times() >= 2, "GetContainerRelativeCgroupPath should be called for each container")
	})
}

func TestDynamicPolicy_checkAllContainersQuota(t *testing.T) {
	t.Parallel()

	// Initialize test object
	policy := &DynamicPolicy{
		metaServer: &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{},
			},
		},
	}

	t.Run("When pod has no containers", func(t *testing.T) {
		t.Parallel()

		// Create Pod without containers
		emptyPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "empty-pod",
				Namespace: "default",
				UID:       types.UID("empty-pod-uid"),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{},
			},
		}

		// Execute test method
		err := policy.checkAllContainersQuota(emptyPod, &common.CgroupResources{})

		// Should handle gracefully
		assert.NoError(t, err)
	})

	t.Run("When pod has containers", func(t *testing.T) {
		t.Parallel()

		// Create test Pod with containers
		testPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				UID:       types.UID("test-pod-uid"),
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "quota-test-container1-id",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("1"),
							},
							Limits: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("2"),
							},
						},
					},
					{
						Name: "quota-test-container2-id",
						Resources: v1.ResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("0.5"),
							},
							Limits: v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("1"),
							},
						},
					},
				},
			},
		}

		// Create test resources
		testResources := &common.CgroupResources{
			CpuQuota: 200000, // 2 CPU cores worth of quota
		}

		// Test the real function
		err := policy.checkAllContainersQuota(testPod, testResources)

		// Verify the result
		assert.NoError(t, err, "checkAllContainersQuota should not return an error")

		// Verify that the mocks were called
		assert.True(t, globalContainerIDMocker.Times() >= 2, "GetContainerID should be called for each container")
		assert.True(t, globalPathMocker.Times() >= 2, "GetContainerRelativeCgroupPath should be called for each container")
		assert.True(t, globalCPUMocker.Times() >= 2, "GetCPUWithRelativePath should be called for each container")
		assert.True(t, globalApplyMocker.Times() >= 2, "ApplyCPUWithRelativePath should be called for each container")
	})
}
