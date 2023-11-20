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

package general

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog/v2"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"
)

const (
	FlockCoolingInterval = 6 * time.Second
	FlockTryLockMaxTimes = 10
)

type FileWatcherInfo struct {
	// if Filename is empty, it means that we should watch all file events in all paths,
	// otherwise, watch this specific file in all paths
	Filename string
	Path     []string
	Op       fsnotify.Op
}

// RegisterFileEventWatcher inotify the given file and report the changed information
// to the caller through returned channel
func RegisterFileEventWatcher(stop <-chan struct{}, fileWatcherInfo FileWatcherInfo) (<-chan struct{}, error) {
	watcherCh := make(chan struct{})

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("new fsNotify watcher failed: %w", err)
	}

	go func() {
		defer func() {
			if err := recover(); err != nil {
				klog.Errorf("RegisterFileEventWatcher panic: %v", err)
			}
		}()

		defer func() {
			close(watcherCh)
			err = watcher.Close()
			if err != nil {
				klog.Errorf("failed close watcher: %v", err)
				return
			}
		}()

		for _, watcherInfoPath := range fileWatcherInfo.Path {
			err = watcher.Add(watcherInfoPath)
			if err != nil {
				klog.Errorf("failed add event path %s: %s", watcherInfoPath, err)
				continue
			}
		}

		for {
			select {
			case event := <-watcher.Events:
				filename := filepath.Base(event.Name)
				if (fileWatcherInfo.Filename == "" || filename == fileWatcherInfo.Filename) &&
					(event.Op&fileWatcherInfo.Op) > 0 {
					klog.Infof("fsNotify watcher notify %s", event)
					watcherCh <- struct{}{}
				}
			case err = <-watcher.Errors:
				klog.Warningf("%v watcher error: %v", fileWatcherInfo, err)
			case <-stop:
				klog.Infof("shutting down event watcher %v", fileWatcherInfo)
				return
			}
		}
	}()

	return watcherCh, nil
}

// GetOneExistPath is to get one of exist paths
func GetOneExistPath(paths []string) string {
	for _, path := range paths {
		if IsPathExists(path) {
			return path
		}
	}
	return ""
}

// IsPathExists is to check this path whether exists
func IsPathExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return true
}

// ReadFileIntoLines read contents from the given file, and parse them into string slice;
// each string indicates a line in the file
func ReadFileIntoLines(filepath string) ([]string, error) {
	lines, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("could not read file %s", filepath)
	}

	var contents []string
	for _, line := range strings.Split(string(lines), "\n") {
		if line == "" {
			continue
		}
		contents = append(contents, line)
	}
	return contents, nil
}

// ReadFileIntoInt read contents from the given file, and parse them into integer
func ReadFileIntoInt(filepath string) (int, error) {
	body, err := ioutil.ReadFile(filepath)
	if err != nil {
		return 0, fmt.Errorf("read file failed with error: %v", err)
	}

	i, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0, fmt.Errorf("convert file content to int failed with error: %v", err)
	}

	return i, nil
}

func EnsureDirectory(dir string) error {
	fs := utilfs.DefaultFs{}
	if _, err := fs.Stat(dir); err != nil {
		// MkdirAll returns nil if directory already exists.
		return fs.MkdirAll(dir, 0755)
	}
	return nil
}

type Flock struct {
	LockFile string
	lock     *os.File
}

func createFlock(file string) (f *Flock, e error) {
	if file == "" {
		e = errors.New("cannot create flock on empty path")
		return
	}
	lock, e := os.Create(file)
	if e != nil {
		return
	}
	return &Flock{
		LockFile: file,
		lock:     lock,
	}, nil
}

func (f *Flock) Release() {
	if f != nil && f.lock != nil {
		_ = f.lock.Close()
	}
}

func (f *Flock) Lock() (e error) {
	if f == nil {
		e = errors.New("cannot use lock on a nil flock")
		return
	}
	return syscall.Flock(int(f.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func (f *Flock) Unlock() {
	if f != nil {
		_ = syscall.Flock(int(f.lock.Fd()), syscall.LOCK_UN)
	}
}

// getUniqueLockWithTimeout try to acquire file lock
// returns the lock struct uf success; otherwise returns error
func getUniqueLockWithTimeout(filename string, duration time.Duration, tries int) (*Flock, error) {
	lockDirPath := path.Dir(filename)
	err := EnsureDirectory(lockDirPath)
	if err != nil {
		klog.Errorf("[GetUniqueLock] ensure lock directory: %s failed with error: %v", lockDirPath, err)
		return nil, err
	}

	lock, err := createFlock(filename)
	if err != nil {
		klog.Errorf("[GetUniqueLock] create lock failed with error: %v", err)
		return nil, err
	}

	tryCount := 0
	for tryCount < tries {
		err = lock.Lock()
		if err == nil {
			break
		}
		tryCount++
		klog.Infof("[GetUniqueLock] try to get unique lock, count: %d", tryCount)
		time.Sleep(duration)
	}

	if err != nil {
		return nil, err
	}

	klog.Infof("[GetUniqueLock] get lock successfully")
	return lock, nil
}

// GetUniqueLock is a wrapper function for getUniqueLockWithTimeout with default configurations
func GetUniqueLock(filename string) (*Flock, error) {
	return getUniqueLockWithTimeout(filename, FlockCoolingInterval, FlockTryLockMaxTimes)
}

// ReleaseUniqueLock release the given file lock
func ReleaseUniqueLock(lock *Flock) {
	if lock == nil {
		return
	}

	lock.Unlock()
	lock.Release()
	klog.Infof("[GetUniqueLock] release lock successfully")
}

func LoadJsonConfig(configAbsPath string, configObject interface{}) error {
	configBytes, err := ioutil.ReadFile(configAbsPath)
	if err != nil {
		return err
	}

	err = json.Unmarshal(configBytes, configObject)
	if err != nil {
		return err
	}

	return nil
}
