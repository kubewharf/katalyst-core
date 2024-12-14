package strategy

const (
	pressureThreshold = 6_000
	easeThreshold     = 1.5 * pressureThreshold
)

func IsResourceUnderPressure(capacity, used int) bool {
	return used+pressureThreshold >= capacity
}

func IsResourceAtEase(capacity, used int) bool {
	return used+easeThreshold < capacity
}
