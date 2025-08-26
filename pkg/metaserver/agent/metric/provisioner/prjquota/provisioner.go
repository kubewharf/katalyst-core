package prjquota

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type FilesystemInfo struct {
	DevicePath string
	MountPoint string
	FsType     string
	Options    string
}

// Define constants related to Linux kernel for ext4
const (
	procMountsPath = "/proc/mounts"

	data00                     = "/data00"
	data00Local                = "/data00/local"
	systemDirectoriesProjectID = 0

	// Define constants related to quotactl, from /usr/include/linux/quota.h
	Q_GETNEXTQUOTA = 0x800009 // Get next quota entry
	PRJQUOTA       = 2        // Project quota type

	// Define constants related to qCMD, from /usr/include/linux/quota.h
	SUBCMDSHIFT = 8
	SUBCMDMASK  = 0x00ff

	quotaBlockSize = 1024
)

// NewPrjquotaMetricsProvisioner returns a new PrjquotaMetricsProvisioner
func NewPrjquotaMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, _ pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	return &PrjquotaMetricsProvisioner{
		metricStore: metricStore,
		emitter:     emitter,
		baseConf:    baseConf,
	}
}

type PrjquotaMetricsProvisioner struct {
	baseConf    *global.BaseConfiguration
	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
}

func (p *PrjquotaMetricsProvisioner) Run(ctx context.Context) {
	p.sample(ctx)
}

func (p *PrjquotaMetricsProvisioner) sample(ctx context.Context) {
	updateTime := time.Now()
	capacity, used, available, err := handlePrjquota(data00, data00Local)
	if err != nil {
		klog.Errorf("failed to get stats from prjquota: %v", err)
		return
	}

	p.metricStore.SetNodeMetric(consts.MetricsNodeFsPrjquotaCapacity, utilmetric.MetricData{Value: float64(capacity), Time: &updateTime})
	p.metricStore.SetNodeMetric(consts.MetricsNodeFsPrjquotaUsed, utilmetric.MetricData{Value: float64(used), Time: &updateTime})
	p.metricStore.SetNodeMetric(consts.MetricsNodeFsPrjquotaAvailable, utilmetric.MetricData{Value: float64(available), Time: &updateTime})
}

// handlePrjquota handle disk calculating of cluster using hostpath lpv
func handlePrjquota(sysPath, localStoragePoolPath string) (capacity, used, available uint64, err error) {
	fileInfo, err := GetFilesystemInfo(sysPath)
	if err != nil {
		klog.Errorf("failed to get device for %v: %v", sysPath, err)
		return 0, 0, 0, err
	}

	if !checkPrjquotaEnabled(fileInfo.Options) {
		return 0, 0, 0, nil
	}

	// get system quota from env if prjquota not set by lpv-driver, otherwise get from real filesystem
	capacity, err = getPrjquotaById(systemDirectoriesProjectID, fileInfo.DevicePath)
	if err != nil {
		klog.Errorf("[prjquota] failed to get prjquota for %v: %v", sysPath, err)
		return 0, 0, 0, err
	}
	if capacity == 0 {
		return 0, 0, 0, nil
	}

	used, err = getDiskUsedBytes(sysPath)
	if err != nil {
		klog.Errorf("[prjquota] failed to get used bytes for %v: %v", sysPath, err)
		return 0, 0, 0, err
	}

	excludeSize := getDirUsedBytes(localStoragePoolPath)
	used -= excludeSize
	if capacity >= used {
		available = capacity - used
	} else {
		available = 0
	}
	klog.Infof("[prjquota] capacity: %v, used: %v, available: %v", capacity, used, available)
	return capacity, used, available, nil
}

// dqblk structure for setting quota limits
type dqblk struct {
	dqbBHardlimit uint64
	dqbBSoftlimit uint64
	dqbCurSpace   uint64
	dqbIHardlimit uint64
	dqbISoftlimit uint64
	dqbCurInodes  uint64
	dqbBTime      uint64
	dqbITime      uint64
	dqbValid      uint32
}

// getPrjquotaById Function to get the quota limit (in bytes) for a specified project ID
func getPrjquotaById(projectID uint32, devicePath string) (uint64, error) {
	bytePtrFromString, err := syscall.BytePtrFromString(devicePath)
	if err != nil {
		return 0, err
	}
	// qCMD Function to construct the qCMD value for quotactl
	qCMD := func(cmd, typ uint32) uint32 {
		return (cmd << SUBCMDSHIFT) | (typ & SUBCMDMASK)
	}
	var dq dqblk
	_, _, errno := unix.Syscall6(
		unix.SYS_QUOTACTL,
		uintptr(qCMD(Q_GETNEXTQUOTA, PRJQUOTA)),
		uintptr(unsafe.Pointer(bytePtrFromString)),
		uintptr(projectID),
		uintptr(unsafe.Pointer(&dq)),
		0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("get project quota information failed (ID %d): %w", projectID, errno)
	}

	return dq.dqbBHardlimit * quotaBlockSize, nil
}

// GetFilesystemInfo retrieves comprehensive filesystem information (combines original two functions)
// Returns: device path, mount point, filesystem type, mount options, error
func GetFilesystemInfo(p string) (fileInfo FilesystemInfo, err error) {
	// Resolve symbolic links to get real path
	resolvedDir, err := filepath.EvalSymlinks(p)
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("resolve symlinks for %s failed: %v", p, err)
	}

	// Get device ID and inode of target directory (for mount point matching)
	var dirStat syscall.Stat_t
	if err := syscall.Stat(resolvedDir, &dirStat); err != nil {
		return FilesystemInfo{}, fmt.Errorf("stat %s failed: %v", resolvedDir, err)
	}
	targetDev := dirStat.Dev
	targetIno := dirStat.Ino

	// Read system mount info (/proc/mounts format: device mountpoint fstype options 0 0)
	mountsFile, err := os.Open(procMountsPath)
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("open %s failed: %v", procMountsPath, err)
	}
	defer mountsFile.Close()

	scanner := bufio.NewScanner(mountsFile)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 { // Require at least 4 fields: device, mountpoint, type, options
			continue
		}
		candidateDevice := fields[0]
		candidateMountPoint := fields[1]
		candidateFsType := fields[2]
		candidateOptions := fields[3]

		// Check if current mount point contains target path (verify by inode comparison)
		var mountStat syscall.Stat_t
		if err := syscall.Stat(candidateMountPoint, &mountStat); err != nil {
			continue // Skip inaccessible mount points
		}

		// Match device ID and check path containment (verify if mount point itself via inode)
		if mountStat.Dev == targetDev && (mountStat.Ino == targetIno || strings.HasPrefix(resolvedDir, candidateMountPoint)) {
			return FilesystemInfo{
				DevicePath: candidateDevice,
				MountPoint: candidateMountPoint,
				FsType:     candidateFsType,
				Options:    candidateOptions,
			}, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return FilesystemInfo{}, fmt.Errorf("scan %s failed: %v", procMountsPath, err)
	}

	return FilesystemInfo{}, fmt.Errorf("no matching filesystem info found for %s (dev: 0x%x)", resolvedDir, targetDev)
}

func checkPrjquotaEnabled(mountOptions string) bool {
	return strings.Contains(mountOptions, "prjquota")
}

func getDirUsedBytes(dir string) uint64 {
	size := uint64(0)
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				klog.Errorf("Walk directory %s failed: %v", path, err)
			}
			return nil
		}
		if !info.IsDir() {
			size += uint64(info.Size())
		}
		return nil
	})

	return size
}

func getDiskUsedBytes(path string) (uint64, error) {
	stat := unix.Statfs_t{}
	err := unix.Statfs(path, &stat)
	if err != nil {
		return 0, err
	}
	return (stat.Blocks - stat.Bavail) * uint64(stat.Bsize), nil
}
