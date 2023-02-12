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

package metric

import (
	"errors"
	"sync"
)

// MetricStore stores those raw metric data items collected from
// agent.MetricsFetcher
type MetricStore struct {
	nodeMetricMap             map[string]float64                                  // map[metricName]value
	numaMetricMap             map[int]map[string]float64                          // map[numaID]map[metricName]value
	deviceMetricMap           map[string]map[string]float64                       // map[deviceName]map[metricName]value
	cpuMetricMap              map[int]map[string]float64                          // map[cpuID]map[metricName]value
	podContainerMetricMap     map[string]map[string]map[string]float64            // map[podUID]map[containerName]map[metricName]value
	podContainerNumaMetricMap map[string]map[string]map[string]map[string]float64 // map[podUID]map[containerName]map[numaNode]map[metricName]value
	mutex                     sync.RWMutex
}

var (
	metricStoreInstance *MetricStore
	metricStoreInitOnce sync.Once
)

// GetMetricStoreInstance is defined as a singleton function to make sure
// only one metric instance is initialized
func GetMetricStoreInstance() *MetricStore {
	metricStoreInitOnce.Do(
		func() {
			metricStoreInstance = &MetricStore{
				nodeMetricMap:             make(map[string]float64),
				numaMetricMap:             make(map[int]map[string]float64),
				deviceMetricMap:           make(map[string]map[string]float64),
				cpuMetricMap:              make(map[int]map[string]float64),
				podContainerMetricMap:     make(map[string]map[string]map[string]float64),
				podContainerNumaMetricMap: make(map[string]map[string]map[string]map[string]float64),
			}
		})
	return metricStoreInstance
}

func (c *MetricStore) SetNodeMetric(metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.nodeMetricMap[metricName] = value
}

func (c *MetricStore) SetNumaMetric(numaID int, metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.numaMetricMap[numaID]; !ok {
		c.numaMetricMap[numaID] = make(map[string]float64)
	}
	c.numaMetricMap[numaID][metricName] = value
}

func (c *MetricStore) SetDeviceMetric(deviceName string, metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.deviceMetricMap[deviceName]; !ok {
		c.deviceMetricMap[deviceName] = make(map[string]float64)
	}
	c.deviceMetricMap[deviceName][metricName] = value
}

func (c *MetricStore) SetCPUMetric(cpuID int, metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.cpuMetricMap[cpuID]; !ok {
		c.cpuMetricMap[cpuID] = make(map[string]float64)
	}
	c.cpuMetricMap[cpuID][metricName] = value
}

func (c *MetricStore) SetContainerMetric(podUID, containerName, metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.podContainerMetricMap[podUID]; !ok {
		c.podContainerMetricMap[podUID] = make(map[string]map[string]float64)
	}

	if _, ok := c.podContainerMetricMap[podUID][containerName]; !ok {
		c.podContainerMetricMap[podUID][containerName] = make(map[string]float64)
	}
	c.podContainerMetricMap[podUID][containerName][metricName] = value
}

func (c *MetricStore) SetContainerNumaMetric(podUID, containerName, numaNode, metricName string, value float64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, ok := c.podContainerNumaMetricMap[podUID]; !ok {
		c.podContainerNumaMetricMap[podUID] = make(map[string]map[string]map[string]float64)
	}

	if _, ok := c.podContainerNumaMetricMap[podUID][containerName]; !ok {
		c.podContainerNumaMetricMap[podUID][containerName] = make(map[string]map[string]float64)
	}

	if _, ok := c.podContainerNumaMetricMap[podUID][containerName][numaNode]; !ok {
		c.podContainerNumaMetricMap[podUID][containerName][numaNode] = make(map[string]float64)
	}
	c.podContainerNumaMetricMap[podUID][containerName][numaNode][metricName] = value
}

func (c *MetricStore) GetNodeMetric(metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if value, ok := c.nodeMetricMap[metricName]; ok {
		return value, nil
	} else {
		return 0, errors.New("[MetricStore] load value failed")
	}
}

func (c *MetricStore) GetNumaMetric(numaID int, metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.numaMetricMap[numaID] != nil {
		if value, ok := c.numaMetricMap[numaID][metricName]; ok {
			return value, nil
		} else {
			return 0, errors.New("[MetricStore] load value failed")
		}
	}
	return 0, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetDeviceMetric(deviceName string, metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.deviceMetricMap[deviceName] != nil {
		if value, ok := c.deviceMetricMap[deviceName][metricName]; ok {
			return value, nil
		} else {
			return 0, errors.New("[MetricStore] load value failed")
		}
	}
	return 0, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetCPUMetric(coreID int, metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.cpuMetricMap[coreID] != nil {
		if value, ok := c.cpuMetricMap[coreID][metricName]; ok {
			return value, nil
		} else {
			return 0, errors.New("[MetricStore] load value failed")
		}
	}
	return 0, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetContainerMetric(podUID, containerName, metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.podContainerMetricMap[podUID] != nil {
		if c.podContainerMetricMap[podUID][containerName] != nil {
			if value, ok := c.podContainerMetricMap[podUID][containerName][metricName]; ok {
				return value, nil
			} else {
				return 0, errors.New("[MetricStore] load value failed")
			}
		}
	}
	return 0, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (float64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.podContainerNumaMetricMap[podUID] != nil {
		if c.podContainerNumaMetricMap[podUID][containerName] != nil {
			if c.podContainerNumaMetricMap[podUID][containerName][numaNode] != nil {
				if value, ok := c.podContainerNumaMetricMap[podUID][containerName][numaNode][metricName]; ok {
					return value, nil
				} else {
					return 0, errors.New("[MetricStore] load value failed")
				}
			}
		}
	}
	return 0, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GCPodsMetric(livingPodUIDSet map[string]bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for podUID := range c.podContainerMetricMap {
		if _, ok := livingPodUIDSet[podUID]; !ok {
			delete(c.podContainerMetricMap, podUID)
			delete(c.podContainerNumaMetricMap, podUID)
		}
	}
}
