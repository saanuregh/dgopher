package audit

// SetTrafficLimits changes how much traffic waits and how much is written
// at once, for a test; the returned func puts them back.
func SetTrafficLimits(max, batch int) func() {
	oldMax, oldBatch := trafficMax, trafficBatch
	trafficMax, trafficBatch = max, batch
	return func() { trafficMax, trafficBatch = oldMax, oldBatch }
}
