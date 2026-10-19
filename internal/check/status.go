package check

// Status is the state shown on the dashboard and the public status page.
type Status string

// Possible statuses, from best to worst; unknown means there are no runs yet.
const (
	StatusOperational Status = "operational"
	StatusDegraded    Status = "degraded"
	StatusDown        Status = "down"
	StatusUnknown     Status = "unknown"
)

// SlowThresholdMS marks a successful run as slow enough to count as degraded.
const SlowThresholdMS = 2000

// StatusFrom derives the current status from the most recent results, newest first.
// Only the last three runs matter so one blip does not mark a service as down.
func StatusFrom(recent []Result) Status {
	if len(recent) == 0 {
		return StatusUnknown
	}
	if len(recent) > 3 {
		recent = recent[:3]
	}
	failed := 0
	for _, r := range recent {
		if !r.OK {
			failed++
		}
	}
	switch {
	case failed == len(recent):
		return StatusDown
	case failed > 0, recent[0].TotalMS > SlowThresholdMS:
		return StatusDegraded
	default:
		return StatusOperational
	}
}

// Overall combines several statuses into the banner of the status page.
func Overall(statuses []Status) Status {
	worst := StatusOperational
	for _, s := range statuses {
		switch s {
		case StatusDown:
			return StatusDown
		case StatusDegraded:
			worst = StatusDegraded
		}
	}
	return worst
}
