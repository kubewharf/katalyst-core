package asyncworker

import (
	"fmt"
)

func (s *workStatus) IsWorking() bool {
	return s.working
}

func validateWork(work *Work) error {
	if work == nil {
		return fmt.Errorf("nil work")
	} else if work.Fn == nil {
		return fmt.Errorf("nil work Fn")
	} else if work.DeliveredAt.IsZero() {
		return fmt.Errorf("zero work DeliveredAt")
	}

	return nil
}
