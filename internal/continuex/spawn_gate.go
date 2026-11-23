package continuex

// Process spawn from Continue paths is off until re-enabled deliberately.
// HeadlessExecutor, ExecRunner, and commandForPlan consult this gate.
var allowProcessSpawn = false

// SetAllowProcessSpawnForTest toggles spawn for unit tests only.
func SetAllowProcessSpawnForTest(on bool) {
	allowProcessSpawn = on
}

func processSpawnAllowed() bool {
	return allowProcessSpawn
}
