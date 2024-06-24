package mbm

import (
	"context"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
)

type controller struct{}

func (c controller) Run(ctx context.Context) {
	//TODO implement me
	panic("implement me")
}

func NewController() agent.Component {
	return &controller{}
}
