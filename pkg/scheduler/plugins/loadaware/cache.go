package loadaware

import (
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

var cache *Cache

func init() {
	cache = &Cache{
		NodePodInfo: map[string]*NodeCache{},
	}
}

type SPDLister interface {
	GetPodPortrait(pod *v1.Pod) *ResourceUsage
}

type Cache struct {
	sync.RWMutex

	// node predict usage cache is calculated by portrait from SPDLister,
	// it is possible to cause errors due to the update of portrait data,
	// but these errors can be tolerated because the update frequency of the portrait is very low and there will not be too much error between the daily data.
	// Reconcile will correct these errors.

	// node predict usage also can be summed when scheduling pod,
	// it makes the results more accurate, but it brings more performance loss.
	podPortraitLister SPDLister

	// key: nodeName, value: NodeCache
	// The value of this map is a pointer, the data of different nodes can be modified concurrently without causing concurrent read/write problems.
	// NodeCache should be locked to ensure that the data of the same node will not be concurrently written, resulting in data coverage.
	NodePodInfo map[string]*NodeCache
}

type NodeCache struct {
	sync.RWMutex

	// key: podUID, value: PodInfo
	PodInfoMap map[string]*PodInfo

	PredictUsage *ResourceUsage
}

type PodInfo struct {
	pod       *v1.Pod
	startTime time.Time
}

type ResourceUsage struct {
	Cpu    []float64
	Memory []float64
}

func (c *Cache) ReconcilePredictUsage() {
	if c.podPortraitLister == nil {
		return
	}

	startTime := time.Now()

	// use read lock here, only adding node and removing node will be blocked by reconcile.
	c.RLock()
	defer c.RUnlock()

	for nodeName, nc := range c.NodePodInfo {
		nc.Lock()

		var (
			nodePredictUsage = &ResourceUsage{
				Cpu:    make([]float64, portraitItemsLength, portraitItemsLength),
				Memory: make([]float64, portraitItemsLength, portraitItemsLength)}
			err error
		)
		for _, podInfo := range nc.PodInfoMap {
			podResourceUsage := c.podPortraitLister.GetPodPortrait(podInfo.pod)
			err = nodePredictUsage.add(podResourceUsage)
			if err != nil {
				klog.Error(err)
				break
			}
			klog.V(6).Infof("ReconcilePredictUsage,pod %v cpu resourceUsage: %v, memory resourceUsage: %v", podInfo.pod.Name, podResourceUsage.Cpu, podResourceUsage.Memory)
		}
		if err != nil {
			klog.Errorf("node %v update predict usage fail: %v, keep old predictUsage", nodeName, err)
			nc.Unlock()
			continue
		}
		nc.PredictUsage = nodePredictUsage
		klog.V(6).Infof("ReconcilePredictUsage, node %v cpu resourceUsage: %v, memory resourceUsage: %v", nodeName, nodePredictUsage.Cpu, nodePredictUsage.Memory)

		nc.Unlock()
	}

	klog.Infof("ReconcilePredictUsage, startTime: %v, duration: %v", startTime, time.Now().Sub(startTime))
}

func (c *Cache) SetSPDLister(lister SPDLister) {
	if c.podPortraitLister != nil {
		klog.Warningf("cache podPortraitLister has been set")
		return
	}

	c.podPortraitLister = lister
}

func (c *Cache) GetNodePredictUsage(nodeName string) *ResourceUsage {
	c.RLock()
	nodeCache, ok := c.NodePodInfo[nodeName]
	c.RUnlock()
	if !ok {
		return &ResourceUsage{
			Cpu:    make([]float64, portraitItemsLength, portraitItemsLength),
			Memory: make([]float64, portraitItemsLength, portraitItemsLength),
		}
	}

	return nodeCache.getPredictUsageCopy()
}

func (c *Cache) addPod(nodeName string, pod *v1.Pod, time time.Time) {
	if pod == nil {
		return
	}

	c.RLock()
	nodeCache, ok := c.NodePodInfo[nodeName]
	c.RUnlock()
	if !ok {
		nodeCache = &NodeCache{
			PodInfoMap: map[string]*PodInfo{},
		}

		c.Lock()
		c.NodePodInfo[nodeName] = nodeCache
		c.Unlock()
	}

	nodeCache.addPod(pod, time, c.podPortraitLister, nodeName)
}

func (c *Cache) removePod(nodeName string, pod *v1.Pod) {
	c.RLock()
	nodeCache, ok := c.NodePodInfo[nodeName]
	c.RUnlock()
	if !ok {
		return
	}

	podNum := nodeCache.removePod(pod, c.podPortraitLister, nodeName)

	c.Lock()
	defer c.Unlock()
	if podNum <= 0 {
		delete(c.NodePodInfo, nodeName)
	}
}

func (nc *NodeCache) addPod(pod *v1.Pod, time time.Time, spdLister SPDLister, nodeName string) {
	podUID := string(pod.UID)
	nc.Lock()
	defer nc.Unlock()

	if _, ok := nc.PodInfoMap[podUID]; ok {
		return
	}

	// update node pod info
	nc.PodInfoMap[podUID] = &PodInfo{
		pod:       pod,
		startTime: time,
	}

	// update node usage if spdLister is not nil
	if spdLister != nil {
		podPortrait := spdLister.GetPodPortrait(pod)
		if nc.PredictUsage == nil {
			nc.PredictUsage = &ResourceUsage{
				Cpu:    make([]float64, portraitItemsLength, portraitItemsLength),
				Memory: make([]float64, portraitItemsLength, portraitItemsLength),
			}
		}

		err := nc.PredictUsage.add(podPortrait)
		if err != nil {
			klog.Errorf("%s nodeCache add pod %v portrait fail: %v", nodeName, pod.Name, err)
			return
		}
		klog.V(6).Infof("node %v add pod %v portrait, cpu: %v, memory: %v", nodeName, pod.Name, podPortrait.Cpu, podPortrait.Memory)
	}
}

func (nc *NodeCache) removePod(pod *v1.Pod, spdLister SPDLister, nodeName string) int {
	podUID := string(pod.UID)
	nc.Lock()
	defer nc.Unlock()

	delete(nc.PodInfoMap, podUID)

	if spdLister != nil {
		podPortrait := spdLister.GetPodPortrait(pod)

		if nc.PredictUsage == nil {
			klog.Errorf("remove pod from node %v without predictUsage", nodeName)
		} else {
			err := nc.PredictUsage.sub(podPortrait)
			if err != nil {
				klog.Errorf("%s nodeCache remove pod %v portrait fail: %v", nodeName, pod.Name, err)
			}
		}
	}
	return len(nc.PodInfoMap)
}

func (nc *NodeCache) getPredictUsageCopy() *ResourceUsage {
	res := &ResourceUsage{
		Cpu:    make([]float64, portraitItemsLength, portraitItemsLength),
		Memory: make([]float64, portraitItemsLength, portraitItemsLength),
	}

	nc.RLock()
	defer nc.RUnlock()
	for i := range nc.PredictUsage.Cpu {
		res.Cpu[i] = nc.PredictUsage.Cpu[i]
		res.Memory[i] = nc.PredictUsage.Memory[i]
	}

	return res
}

func (r *ResourceUsage) add(data *ResourceUsage) error {
	if len(data.Cpu) != portraitItemsLength || len(data.Memory) != portraitItemsLength {
		return fmt.Errorf("portrait data length cpu: %v memory: %v not support", len(data.Cpu), len(data.Memory))
	}

	for i := 0; i < portraitItemsLength; i++ {
		r.Cpu[i] += data.Cpu[i]
		r.Memory[i] += data.Memory[i]
	}

	return nil
}

func (r *ResourceUsage) sub(data *ResourceUsage) error {
	if len(data.Cpu) != portraitItemsLength || len(data.Memory) != portraitItemsLength {
		return fmt.Errorf("portrait data length cpu: %v memory: %v not support", len(data.Cpu), len(data.Memory))
	}

	for i := 0; i < portraitItemsLength; i++ {
		r.Cpu[i] -= data.Cpu[i]
		r.Memory[i] -= data.Memory[i]

		if r.Cpu[i] < 0 {
			r.Cpu[i] = 0
		}
		if r.Memory[i] < 0 {
			r.Memory[i] = 0
		}
	}

	return nil
}

func (r *ResourceUsage) max(resourceName v1.ResourceName) float64 {
	var (
		data []float64
		res  float64
	)

	switch resourceName {
	case v1.ResourceCPU:
		data = r.Cpu
	case v1.ResourceMemory:
		data = r.Memory
	default:
		klog.Warningf("unsupported resource %v", resourceName.String())
		return res
	}

	for i := range data {
		if data[i] > res {
			res = data[i]
		}
	}
	return res
}
