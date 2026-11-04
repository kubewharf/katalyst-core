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
	"io/fs"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/apimachinery/pkg/util/sets"
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

// RegisterMultipleFileEventWatchers registers a watcher for each FileWatcherInfo
// by delegating to RegisterFileEventWatcher, and merges the per-watcher signals
// into a single returned channel. The returned channel receives a signal
// whenever any of the underlying watchers fires, and is closed once all
// underlying watchers have stopped (typically after stop is closed).
func RegisterMultipleFileEventWatchers(stop <-chan struct{}, fileWatcherInfos ...FileWatcherInfo) (<-chan struct{}, error) {
	aggregatedCh := make(chan struct{})

	// register each underlying watcher up-front so any setup error is returned synchronously.
	subChs := make([]<-chan struct{}, 0, len(fileWatcherInfos))
	for _, info := range fileWatcherInfos {
		ch, err := RegisterFileEventWatcher(stop, info)
		if err != nil {
			return nil, fmt.Errorf("register file event watcher for %v failed: %w", info, err)
		}
		subChs = append(subChs, ch)
	}

	// one forwarder goroutine per sub-channel fans signals into aggregatedCh.
	var wg sync.WaitGroup
	for _, ch := range subChs {
		wg.Add(1)
		go func(c <-chan struct{}) {
			defer wg.Done()
			for {
				select {
				case _, ok := <-c:
					if !ok {
						return
					}
					// inner <-stop is required: aggregatedCh is unbuffered, so
					// without it a slow/absent consumer would deadlock the forwarder
					// after stop is closed.
					select {
					case aggregatedCh <- struct{}{}:
					case <-stop:
						return
					}
				case <-stop:
					return
				}
			}
		}(ch)
	}

	// close aggregatedCh only after every forwarder has exited so callers can range over it safely.
	go func() {
		wg.Wait()
		close(aggregatedCh)
	}()

	return aggregatedCh, nil
}

// SubDirWatcherInfo describes a directory tree (root + first-level children) to watch.
// It is used by RegisterSubDirEventWatcher to monitor pod-cgroup-like layouts where:
//   - a root path is watched for child directory creation/removal,
//   - first-level child directories under each root are also watched and
//     dynamically added/removed as the tree changes.
type SubDirWatcherInfo struct {
	// RootPaths are the parent directories whose first-level subdirectories
	// should be discovered and watched. Each root itself is also watched so
	// that newly created / removed subdirectories can be tracked.
	RootPaths []string
}

// DirWatchListFunc returns a snapshot of the paths currently being watched
// (both root paths and discovered first-level children), sorted to keep log
// output stable. It is goroutine safe and may be called from any goroutine.
// After the watcher is stopped, the returned slice is best-effort and may be
// empty or contain a stale snapshot, depending on the underlying fsnotify
// implementation.
type DirWatchListFunc func() []string

// RegisterSubDirEventWatcher watches a set of root directories together with
// their first-level child directories, and reports a "needs-sync" signal to
// the caller through the returned channel whenever a create / remove event
// happens on a watched root or one of its child directories.
//
// Behavior:
//   - On startup every root in watcherInfo.RootPaths is added to the watcher and
//     its first-level child directories are discovered via filepath.WalkDir
//     and also added.
//   - At runtime, when a Create event fires on a path whose parent is a
//     watched root and the new path is a directory, the new child is added
//     to the watcher set.
//   - When a Remove event fires on a previously watched child, the child is
//     removed from the watcher set.
//   - Any Create / Remove event hitting a watched root or child triggers a
//     send on the returned channel. Sends are non-blocking: if the receiver
//     is slow, events may be coalesced (the channel is buffered with size 1).
//   - On <-stop, the underlying fsnotify watcher is closed and the returned
//     channel is closed.
//
// The returned DirWatchListFunc gives the caller an observability hook to dump
// the currently watched paths (mirroring fsnotify.Watcher.WatchList()).
//
// The caller is responsible for any rate limiting / periodic fallback /
// actual sync behavior on top of the returned signal channel.
func RegisterSubDirEventWatcher(stop <-chan struct{}, watcherInfo SubDirWatcherInfo) (<-chan struct{}, DirWatchListFunc, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, fmt.Errorf("new fsNotify watcher failed: %w", err)
	}

	notifyCh := make(chan struct{}, 1)

	// rootPaths and childPaths are only accessed from the watcher goroutine
	// below, so they don't need synchronization.
	rootPaths := sets.NewString()
	childPaths := sets.NewString()

	// watchList delegates to the underlying fsnotify watcher, which is safe
	// for concurrent use. After the watcher is closed, the returned slice is
	// best-effort and may be empty or contain a stale snapshot, depending on
	// the underlying fsnotify implementation.
	watchList := watcher.WatchList

	addChild := func(p string) {
		p = filepath.Clean(p)
		if childPaths.Has(p) {
			return
		}
		if err := watcher.Add(p); err != nil {
			klog.Errorf("failed add watch path %s: %v", p, err)
			return
		}
		childPaths.Insert(p)
	}

	removeChild := func(p string) bool {
		p = filepath.Clean(p)
		if !childPaths.Has(p) {
			return false
		}
		if err := watcher.Remove(p); err != nil {
			klog.ErrorS(err, "failed remove event path", "path", p)
		}
		childPaths.Delete(p)
		return true
	}

	notify := func() {
		select {
		case notifyCh <- struct{}{}:
		default:
		}
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				klog.Errorf("RegisterSubDirEventWatcher panic: %v", r)
			}
		}()

		defer func() {
			close(notifyCh)
			if cerr := watcher.Close(); cerr != nil {
				klog.Errorf("failed close watcher: %v", cerr)
			}
		}()

		for _, rootPath := range watcherInfo.RootPaths {
			rootPath = filepath.Clean(rootPath)
			if err := watcher.Add(rootPath); err != nil {
				klog.Errorf("failed add event path %s: %s", rootPath, err)
				continue
			}
			rootPaths.Insert(rootPath)

			if err := filepath.WalkDir(rootPath, func(p string, info fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p == rootPath {
					return nil
				}
				if info.IsDir() {
					klog.Infof("walk dir: %s", p)
					addChild(p)
					return filepath.SkipDir
				}
				return nil
			}); err != nil {
				klog.ErrorS(err, "failed to walkDir", "path", rootPath)
			}
		}

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				eventPath := filepath.Clean(event.Name)
				needSync := false
				if rootPaths.Has(filepath.Dir(eventPath)) {
					if event.Op&fsnotify.Create != 0 {
						info, statErr := os.Stat(eventPath)
						if statErr != nil {
							klog.ErrorS(statErr, "failed stat event path", "path", eventPath)
						} else if info.IsDir() {
							addChild(eventPath)
							needSync = true
						}
					} else if event.Op&fsnotify.Remove != 0 {
						if removeChild(eventPath) {
							needSync = true
						}
					}
				}
				if event.Op&(fsnotify.Create|fsnotify.Remove) != 0 &&
					(needSync || rootPaths.Has(eventPath) || childPaths.Has(eventPath)) {
					klog.InfoS("sub-dir watcher path event", "event", event.String())
					notify()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				klog.Warningf("sub-dir watcher error: %v", err)
			case <-stop:
				klog.Infof("shutting down sub-dir event watcher")
				return
			}
		}
	}()

	return notifyCh, watchList, nil
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
