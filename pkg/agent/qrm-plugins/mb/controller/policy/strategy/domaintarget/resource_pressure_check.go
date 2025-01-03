package domaintarget

import policyconfig "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/config"

func IsResourceUnderPressure(capacity, used int) bool {
	return used+policyconfig.PolicyConfig.PressureThreshold >= capacity
}

func IsResourceAtEase(capacity, used int) bool {
	return used+policyconfig.PolicyConfig.EaseThreshold < capacity
}
