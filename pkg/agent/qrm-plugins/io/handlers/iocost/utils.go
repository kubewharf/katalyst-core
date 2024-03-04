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

package iocost

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	containerdConfigFile = "/root/tce/containerd/conf/containerd.toml"
)

func getDevicesIdToModel(deviceNames []string) (map[string]DevModel, error) {
	devsIDToModel := make(map[string]DevModel, len(deviceNames))
	for _, devName := range deviceNames {
		_, err := os.Stat(fmt.Sprintf("/sys/block/%s/device", devName))
		if os.IsNotExist(err) {
			continue
		}

		devIDFile := fmt.Sprintf("/sys/block/%s/dev", devName)
		devIDBytes, err := ioutil.ReadFile(devIDFile)
		if err != nil {
			general.Errorf("failed to ReadFile %s, err %v", devIDFile, err)
			continue
		}
		devID := strings.TrimSpace(string(devIDBytes))

		modelBytes, err := ioutil.ReadFile(fmt.Sprintf("/sys/block/%s/device/model", devName))
		if err != nil {
			if strings.HasPrefix(devName, "vd") {
				devsIDToModel[devID] = DevModelVirtualdisk
			}
			continue
		}

		model := strings.TrimSpace(string(modelBytes))
		if strings.HasPrefix(strings.ToLower(model), "qemu") {
			devsIDToModel[devID] = DevModelVirtualdisk
		} else {
			devsIDToModel[devID] = DevModel(model)
		}
	}

	return devsIDToModel, nil
}

func getAllDeviceNames() ([]string, error) {
	files, err := ioutil.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	allDeviceNames := make([]string, 0, len(files))
	for _, fi := range files {
		allDeviceNames = append(allDeviceNames, fi.Name())
	}

	return allDeviceNames, nil
}

func getDeviceNameFromID(targetDevID string) (string, bool, error) {
	allDeviceNames, err := getAllDeviceNames()
	if err != nil {
		return "", false, err
	}

	for _, devName := range allDeviceNames {
		_, err := os.Stat(fmt.Sprintf("/sys/block/%s/device", devName))
		if os.IsNotExist(err) {
			continue
		}

		devIDFile := fmt.Sprintf("/sys/block/%s/dev", devName)
		devIDBytes, err := ioutil.ReadFile(devIDFile)
		if err != nil {
			general.Errorf("failed to ReadFile %s, err %v", devIDFile, err)
			continue
		}
		devID := strings.TrimSpace(string(devIDBytes))

		if devID == targetDevID {
			return devName, true, nil
		}
	}

	return "", false, nil
}

func getContainerdRootDir() (string, error) {
	lines, err := general.ReadFileIntoLines(containerdConfigFile)
	if err != nil {
		return "", fmt.Errorf("failed to ReadLines %s, err %v", containerdConfigFile, err)
	}

	var rootDir string
	for _, line := range lines {
		cols := strings.Split(line, "=")
		if len(cols) != 2 {
			continue
		}

		if strings.TrimSpace(cols[0]) == "root" {
			rootDir = strings.TrimSpace(cols[1])
			rootDir = strings.TrimPrefix(rootDir, "\"")
			rootDir = strings.TrimSuffix(rootDir, "\"")
			rootDir = strings.TrimSpace(rootDir)
			break
		}
	}

	if rootDir == "" {
		return "", fmt.Errorf("failed to find root config in %s", containerdConfigFile)
	}

	return rootDir, nil
}

func isHDD(deviceName, rotationalFilePath string) (bool, error) {
	/* Check if the device name starts with "sd"
	 * sd means scsi devices.
	 * Currently, only HDD/SSD could be scsi device.
	 */
	// Step1, the device should be scsi device.
	if !strings.HasPrefix(deviceName, "sd") {
		return false, fmt.Errorf("not scsi disk")
	}

	// Step2, if it is scsi device, then check rotational
	// if rotational = 1, then HDD, else SSD.
	cleanedRotationalFilePath := filepath.Clean(rotationalFilePath)
	contents, err := ioutil.ReadFile(cleanedRotationalFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Parse rotational status (1 means rotational, 0 means non-rotational)
	rotational := strings.TrimSpace(string(contents))
	return rotational == "1", nil
}
