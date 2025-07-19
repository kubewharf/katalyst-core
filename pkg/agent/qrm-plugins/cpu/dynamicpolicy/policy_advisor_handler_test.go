package dynamicpolicy

import (
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"

	"github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
)

func TestDynamicPolicy_getAllPodsPathMap(t *testing.T) {
	Convey("Test getAllPodsPathMap", t, func() {
		// 初始化测试对象
		policy := &DynamicPolicy{
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &pod.PodFetcherStub{},
				},
			},
		}

		// 模拟Pod数据
		mockPods := []*v1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec:       v1.PodSpec{NodeName: "node-1"},
			},
		}

		mockey.Mock((*pod.PodFetcherStub).GetPodList).Return(mockPods, nil).Build()
		mockey.Mock(common.GetPodAbsCgroupPath).Return("test_pod_path", nil).Build()

		// 执行测试方法
		resultMap, err := policy.getAllPodsPathMap()

		// 验证结果
		So(err, ShouldBeNil)
		So(resultMap, ShouldNotBeNil)
		So(len(resultMap), ShouldEqual, 1)
		So(resultMap["test_pod_path"].Name, ShouldEqual, "test-pod")
	})
}

// mockDirEntry 是一个模拟的 fs.DirEntry 实现
type mockDirEntry struct {
	name  string
	isDir bool
}

func (m *mockDirEntry) Name() string               { return m.name }
func (m *mockDirEntry) IsDir() bool                { return m.isDir }
func (m *mockDirEntry) Type() fs.FileMode          { return 0 }
func (m *mockDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestDynamicPolicy_getAllDirs(t *testing.T) {
	Convey("Test getAllDirs", t, func() {
		// 初始化测试对象
		policy := &DynamicPolicy{
			metaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{
					PodFetcher: &pod.PodFetcherStub{},
				},
			},
		}

		mockey.PatchConvey("When ReadDir succeeds and returns directories", func() {
			// 模拟目录条目
			mockEntries := []fs.DirEntry{
				&mockDirEntry{name: "dir1", isDir: true},
				&mockDirEntry{name: "file1", isDir: false},
				&mockDirEntry{name: "dir2", isDir: true},
				&mockDirEntry{name: "file2", isDir: false},
				&mockDirEntry{name: "dir3", isDir: true},
			}

			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// 执行测试方法
			result, err := policy.getAllDirs("/test/path")

			// 验证结果
			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			So(len(result), ShouldEqual, 3)
			So(result, ShouldContain, "dir1")
			So(result, ShouldContain, "dir2")
			So(result, ShouldContain, "dir3")
			So(result, ShouldNotContain, "file1")
			So(result, ShouldNotContain, "file2")
		})

		mockey.PatchConvey("When ReadDir succeeds but returns no directories", func() {
			// 模拟只有文件的目录条目
			mockEntries := []fs.DirEntry{
				&mockDirEntry{name: "file1", isDir: false},
				&mockDirEntry{name: "file2", isDir: false},
			}

			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// 执行测试方法
			result, err := policy.getAllDirs("/test/path")

			// 验证结果
			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			So(len(result), ShouldEqual, 0)
		})

		mockey.PatchConvey("When ReadDir succeeds but returns empty directory", func() {
			// 模拟空目录
			mockEntries := []fs.DirEntry{}

			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// 执行测试方法
			result, err := policy.getAllDirs("/test/path")

			// 验证结果
			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			So(len(result), ShouldEqual, 0)
		})

		mockey.PatchConvey("When ReadDir fails", func() {
			// 模拟 ReadDir 失败
			expectedErr := fmt.Errorf("permission denied")
			mockey.Mock(os.ReadDir).Return(nil, expectedErr).Build()

			// 执行测试方法
			result, err := policy.getAllDirs("/test/path")

			// 验证结果
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldEqual, expectedErr.Error())
			So(result, ShouldBeNil)
		})

		mockey.PatchConvey("When ReadDir returns mixed content", func() {
			// 模拟混合内容（目录、文件、符号链接等）
			mockEntries := []fs.DirEntry{
				&mockDirEntry{name: "normal_dir", isDir: true},
				&mockDirEntry{name: "hidden_dir", isDir: true},
				&mockDirEntry{name: "regular_file", isDir: false},
				&mockDirEntry{name: "another_dir", isDir: true},
			}

			mockey.Mock(os.ReadDir).Return(mockEntries, nil).Build()

			// 执行测试方法
			result, err := policy.getAllDirs("/test/path")

			// 验证结果
			So(err, ShouldBeNil)
			So(result, ShouldNotBeNil)
			So(len(result), ShouldEqual, 3)
			So(result, ShouldContain, "normal_dir")
			So(result, ShouldContain, "hidden_dir")
			So(result, ShouldContain, "another_dir")
			So(result, ShouldNotContain, "regular_file")
		})
	})
}
