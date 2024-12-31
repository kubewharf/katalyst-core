package domaintarget

const (
	PressureThreshold = 6_000
	easeThreshold     = 1.5 * PressureThreshold
)

func IsResourceUnderPressure(capacity, used int) bool {
	return used+PressureThreshold >= capacity
}

func IsResourceAtEase(capacity, used int) bool {
	return used+easeThreshold < capacity
}
