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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog/v2"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"
)

const (
	FlockCoolingInterval                = 6 * time.Second
	FlockTryLockMaxTimes                = 10
	ModificationTimeDifferenceThreshold = 2 * time.Second
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

// GetOneExistPathUntilExist returns a path until one provided path exists
func GetOneExistPathUntilExist(
	paths []string, checkInterval,
	timeoutDuration time.Duration,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDuration)
	defer cancel()

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timeout reached while waiting for an existing path")
		case <-ticker.C:
			if p := GetOneExistPath(paths); p != "" {
				return p, nil
			}
		}
	}
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
		return nil, err
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
		return fs.MkdirAll(dir, 0o755)
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

func ReadLines(file string) ([]string, error) {
	f, err := os.OpenFile(file, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := make([]string, 0)
	scanner := bufio.NewScanner(f)

	maxCapacity := 1024 * 1024
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func ReadInt64FromFile(file string) (int64, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return -1, fmt.Errorf("failed to read(%s), err %v", file, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))

	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("failed to ParseInt(%s), err %v", s, err)
	}
	return val, nil
}

func ReadUint64FromFile(file string) (uint64, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("failed to read(%s), err %v", file, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))

	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to ParseInt(%s), err %v", s, err)
	}
	return val, nil
}

func GetFileInode(file string) (uint64, error) {
	fileInfo, err := os.Stat(file)
	if err != nil {
		return 0, fmt.Errorf("failed to stat(%s), err %v", file, err)
	}

	// Type assertion to get syscall.Stat_t which contains inode information
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unable to get inode information for %s", file)
	}

	return stat.Ino, nil
}

func ParseLinuxListFormatFromFile(filePath string) ([]int64, error) {
	b, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to ReadFile %s, err %s", filePath, err)
	}

	s := strings.TrimSpace(strings.TrimRight(string(b), "\n"))
	if len(s) == 0 {
		return nil, nil
	}
	return ParseLinuxListFormat(s)
}

// JSONFilesEqual unmarshals the contents of JSON files into structs and checks if they are identical
func JSONFilesEqual(path1, path2 string) (bool, error) {
	decode := func(path string) (interface{}, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open file %s: %w", path, err)
		}
		defer f.Close()
		var obj interface{}
		if err := json.NewDecoder(f).Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return obj, nil
			}
			return nil, fmt.Errorf("failed to decode file %s: %w", path, err)
		}
		return obj, nil
	}

	obj1, err := decode(path1)
	if err != nil {
		return false, err
	}
	obj2, err := decode(path2)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(obj1, obj2), nil
}

// IsFileUpToDate checks if the target file is updated by comparing its last modification time with the other file
// The modification time of the target file has to fulfill any of the following conditions to be considered up to date:
// 1. Be updated more recently than the other file
// 2. Fall within a threshold difference of the other file's modification time
func IsFileUpToDate(targetFilePath string, otherFilePath string) (bool, error) {
	targetInfo, err := os.Stat(targetFilePath)
	if err != nil {
		return false, fmt.Errorf("failed to stat target file %s, err %v", targetFilePath, err)
	}
	otherInfo, err := os.Stat(otherFilePath)
	if err != nil {
		return false, fmt.Errorf("failed to stat other file %s, err %v", otherFilePath, err)
	}
	targetModTime := targetInfo.ModTime()
	otherModTime := otherInfo.ModTime()

	// Target file is updated more recently than the other file
	if targetModTime.After(otherModTime) {
		return true, nil
	}

	// Check if the modification time of the target file is within the threshold difference of the other file's modification time
	if otherModTime.Sub(targetModTime).Seconds() <= ModificationTimeDifferenceThreshold.Seconds() {
		return true, nil
	}
	return false, nil
}
