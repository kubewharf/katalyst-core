package loadaware

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config/validation"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	Name                 = "LoadAware"
	loadAwareMetricScope = "loadAware"

	DefaultNPDReportInterval       = 60 * time.Second
	DefaultMilliCPURequest   int64 = 250               // 0.25 core
	DefaultMemoryRequest     int64 = 200 * 1024 * 1024 // 200 MB
)

var (
	_ framework.FilterPlugin  = &Plugin{}
	_ framework.ScorePlugin   = &Plugin{}
	_ framework.ReservePlugin = &Plugin{}
)

type Plugin struct {
	handle    framework.Handle
	args      *config.LoadAwareArgs
	npdLister listers.NodeProfileDescriptorLister
}

func NewPlugin(args runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	klog.Infof("new loadAware scheduler plugin")
	pluginArgs, ok := args.(*config.LoadAwareArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type LoadAwareArgs, got %T", args)
	}
	if err := validation.ValidateLoadAwareSchedulingArgs(pluginArgs); err != nil {
		klog.Errorf("validate pluginArgs fail, err: %v", err)
		return nil, err
	}

	p := &Plugin{
		handle: handle,
		args:   pluginArgs,
	}
	p.registerNodeMonitorHandler()
	RegisterPodHandler()

	return p, nil
}

func (p *Plugin) Name() string {
	return Name
}

func (p *Plugin) IsLoadAwareEnabled(pod *v1.Pod) bool {
	if p.args.PodAnnotationLoadAwareEnable == nil || *p.args.PodAnnotationLoadAwareEnable == "" {
		return true
	}

	if flag, ok := pod.Annotations[*p.args.PodAnnotationLoadAwareEnable]; ok && flag == consts.PodAnnotationLoadAwareEnableTrue {
		return true
	}
	return false
}
