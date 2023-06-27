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
	"fmt"
	"sync"
	"time"
)

// MetricData represents the standard response data for metric getter functions
type MetricData struct {
	Value float64

	// Time may have different meanings in different scenarios
	// - for single metric: it represents the exact collecting time
	// - for aggregated metric: it represents the newest time among all metric items
	Time *time.Time
}

// MetricStore stores those raw metric data items collected from
// agent.MetricsFetcher
type MetricStore struct {
	mutex sync.RWMutex

	nodeMetricMap             map[string]MetricData                                  // map[metricName]data
	numaMetricMap             map[int]map[string]MetricData                          // map[numaID]map[metricName]data
	deviceMetricMap           map[string]map[string]MetricData                       // map[deviceName]map[metricName]data
	cpuMetricMap              map[int]map[string]MetricData                          // map[cpuID]map[metricName]data
	podContainerMetricMap     map[string]map[string]map[string]MetricData            // map[podUID]map[containerName]map[metricName]data
	podContainerNumaMetricMap map[string]map[string]map[string]map[string]MetricData // map[podUID]map[containerName]map[numaNode]map[metricName]data
	qosClassMetricMap         map[string]map[string]MetricData                       // map[qosClass]map[metricName]value
	qosClassNumaMetricMap     map[string]map[string]map[string]MetricData            // map[qosClass]map[numaNode]map[metricName]value
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
				nodeMetricMap:             make(map[string]MetricData),
				numaMetricMap:             make(map[int]map[string]MetricData),
				deviceMetricMap:           make(map[string]map[string]MetricData),
				cpuMetricMap:              make(map[int]map[string]MetricData),
				podContainerMetricMap:     make(map[string]map[string]map[string]MetricData),
				podContainerNumaMetricMap: make(map[string]map[string]map[string]map[string]MetricData),
				qosClassMetricMap:         make(map[string]map[string]MetricData),
				qosClassNumaMetricMap:     make(map[string]map[string]map[string]MetricData),
			}
		})
	return metricStoreInstance
}

func (c *MetricStore) SetNodeMetric(metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.nodeMetricMap[metricName] = data
}

func (c *MetricStore) SetNumaMetric(numaID int, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.numaMetricMap[numaID]; !ok {
		c.numaMetricMap[numaID] = make(map[string]MetricData)
	}
	c.numaMetricMap[numaID][metricName] = data
}

func (c *MetricStore) SetDeviceMetric(deviceName string, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.deviceMetricMap[deviceName]; !ok {
		c.deviceMetricMap[deviceName] = make(map[string]MetricData)
	}
	c.deviceMetricMap[deviceName][metricName] = data
}

func (c *MetricStore) SetCPUMetric(cpuID int, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.cpuMetricMap[cpuID]; !ok {
		c.cpuMetricMap[cpuID] = make(map[string]MetricData)
	}
	c.cpuMetricMap[cpuID][metricName] = data
}

func (c *MetricStore) SetContainerMetric(podUID, containerName, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if _, ok := c.podContainerMetricMap[podUID]; !ok {
		c.podContainerMetricMap[podUID] = make(map[string]map[string]MetricData)
	}

	if _, ok := c.podContainerMetricMap[podUID][containerName]; !ok {
		c.podContainerMetricMap[podUID][containerName] = make(map[string]MetricData)
	}
	c.podContainerMetricMap[podUID][containerName][metricName] = data
}

func (c *MetricStore) SetContainerNumaMetric(podUID, containerName, numaNode, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, ok := c.podContainerNumaMetricMap[podUID]; !ok {
		c.podContainerNumaMetricMap[podUID] = make(map[string]map[string]map[string]MetricData)
	}

	if _, ok := c.podContainerNumaMetricMap[podUID][containerName]; !ok {
		c.podContainerNumaMetricMap[podUID][containerName] = make(map[string]map[string]MetricData)
	}

	if _, ok := c.podContainerNumaMetricMap[podUID][containerName][numaNode]; !ok {
		c.podContainerNumaMetricMap[podUID][containerName][numaNode] = make(map[string]MetricData)
	}
	c.podContainerNumaMetricMap[podUID][containerName][numaNode][metricName] = data
}

func (c *MetricStore) GetNodeMetric(metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if data, ok := c.nodeMetricMap[metricName]; ok {
		return data, nil
	} else {
		return MetricData{}, errors.New("[MetricStore] load value failed")
	}
}

func (c *MetricStore) GetNumaMetric(numaID int, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.numaMetricMap[numaID] != nil {
		if data, ok := c.numaMetricMap[numaID][metricName]; ok {
			return data, nil
		} else {
			return MetricData{}, errors.New("[MetricStore] load value failed")
		}
	}
	return MetricData{}, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetDeviceMetric(deviceName string, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.deviceMetricMap[deviceName] != nil {
		if data, ok := c.deviceMetricMap[deviceName][metricName]; ok {
			return data, nil
		} else {
			return MetricData{}, errors.New("[MetricStore] load value failed")
		}
	}
	return MetricData{}, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetCPUMetric(coreID int, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.cpuMetricMap[coreID] != nil {
		if data, ok := c.cpuMetricMap[coreID][metricName]; ok {
			return data, nil
		} else {
			return MetricData{}, errors.New("[MetricStore] load value failed")
		}
	}
	return MetricData{}, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetContainerMetric(podUID, containerName, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.podContainerMetricMap[podUID] != nil {
		if c.podContainerMetricMap[podUID][containerName] != nil {
			if data, ok := c.podContainerMetricMap[podUID][containerName][metricName]; ok {
				return data, nil
			} else {
				return MetricData{}, errors.New("[MetricStore] load value failed")
			}
		}
	}
	return MetricData{}, errors.New("[MetricStore] empty map")
}

func (c *MetricStore) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if c.podContainerNumaMetricMap[podUID] != nil {
		if c.podContainerNumaMetricMap[podUID][containerName] != nil {
			if c.podContainerNumaMetricMap[podUID][containerName][numaNode] != nil {
				if data, ok := c.podContainerNumaMetricMap[podUID][containerName][numaNode][metricName]; ok {
					return data, nil
				} else {
					return MetricData{}, errors.New("[MetricStore] load value failed")
				}
			}
		}
	}
	return MetricData{}, errors.New("[MetricStore] empty map")
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

func (c *MetricStore) SetQosClassMetric(qosClass, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	metrics, ok := c.qosClassMetricMap[qosClass]
	if !ok {
		metrics = make(map[string]MetricData)
		c.qosClassMetricMap[qosClass] = metrics
	}
	metrics[metricName] = data
}

func (c *MetricStore) GetQosClassMetric(qosClass, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	metrics, ok := c.qosClassMetricMap[qosClass]
	if !ok {
		return MetricData{}, fmt.Errorf("[MetricStore] load value for %v failed", qosClass)
	}
	data, ok := metrics[metricName]
	if !ok {
		return MetricData{}, fmt.Errorf("[MetricStore] load value for %v failed", metricName)
	}
	return data, nil
}

func (c *MetricStore) SetQosClassNumaMetric(qosClass, numaNode, metricName string, data MetricData) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	numaMetrics, ok := c.qosClassNumaMetricMap[qosClass]
	if !ok {
		numaMetrics = make(map[string]map[string]MetricData)
		c.qosClassNumaMetricMap[qosClass] = numaMetrics
	}
	metrics, ok := numaMetrics[numaNode]
	if !ok {
		metrics = make(map[string]MetricData)
		numaMetrics[numaNode] = metrics
	}
	metrics[metricName] = data
}

func (c *MetricStore) GetQosClassNumaMetric(qosClass, numaNode, metricName string) (MetricData, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	numaMetrics, ok := c.qosClassNumaMetricMap[qosClass]
	if !ok {
		return MetricData{}, fmt.Errorf("[MetricStore] load value for %v failed", qosClass)
	}
	metrics, ok := numaMetrics[numaNode]
	if !ok {
		return MetricData{}, fmt.Errorf("[MetricStore] load value for %v failed", numaNode)
	}
	metric, ok := metrics[metricName]
	if !ok {
		return MetricData{}, fmt.Errorf("[MetricStore] load value for %v failed", metricName)
	}
	return metric, nil
}
