package localapp

import "errors"

// RestartExitCode tells the desktop supervisor to start a fresh native process.
const RestartExitCode = 75

var ErrRestart = errors.New("desktop node restart requested")
