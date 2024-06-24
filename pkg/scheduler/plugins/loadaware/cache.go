package loadaware

import (
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
)

var cache *Cache

func init() {
	cache = &Cache{
		NodePodInfo: map[string]*NodeCache{},
	}
}

type Cache struct {
	sync.RWMutex

	// key: nodeName, value: NodeCache
	NodePodInfo map[string]*NodeCache
}

type NodeCache struct {
	// key: podUID, value: PodInfo
	PodInfoMap map[string]*PodInfo
}

type PodInfo struct {
	pod       *v1.Pod
	startTime time.Time
}

func (c *Cache) addPod(nodeName string, pod *v1.Pod, time time.Time) {
	if pod == nil {
		return
	}
	podUID := string(pod.UID)
	c.Lock()
	defer c.Unlock()

	nodeCache, ok := c.NodePodInfo[nodeName]
	if !ok {
		nodeCache = &NodeCache{
			PodInfoMap: map[string]*PodInfo{},
		}
	}

	_, ok = nodeCache.PodInfoMap[podUID]
	if ok {
		return
	}

	nodeCache.PodInfoMap[podUID] = &PodInfo{
		pod:       pod,
		startTime: time,
	}
	c.NodePodInfo[nodeName] = nodeCache
}

func (c *Cache) removePod(nodeName string, pod *v1.Pod) {
	c.Lock()
	defer c.Unlock()
	nodeCache, ok := c.NodePodInfo[nodeName]
	if !ok {
		return
	}

	delete(nodeCache.PodInfoMap, string(pod.UID))
	if len(nodeCache.PodInfoMap) <= 0 {
		delete(c.NodePodInfo, nodeName)
	} else {
		c.NodePodInfo[nodeName] = nodeCache
	}
}
