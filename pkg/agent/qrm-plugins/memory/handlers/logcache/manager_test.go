package logcache

import (
	"k8s.io/apimachinery/pkg/util/wait"
	"testing"
	"time"
)

func TestFileCacheEvictionManagerDoEviction(t *testing.T) {
	mgr := fileCacheEvictionManager{
		highThresholdGB: 30,
		lowThresholdGB:  10,
		minInterval:     time.Second,
		maxInterval:     time.Second * 120,
		curInterval:     time.Second,
		lastEvictTime:   nil,
		pathList:        []string{"."},
		fileFilters:     []string{".*.log.*"},
	}

	stopCh := make(chan struct{})
	go func() {
		time.Sleep(time.Minute * 4)
		stopCh <- struct{}{}
	}()

	go wait.Until(mgr.doEviction, time.Second, stopCh)
	<-stopCh
}
