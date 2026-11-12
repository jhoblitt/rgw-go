package op

// NumAuthRoutesForTest is the number of AuthRoute values, so that a spec can
// require an entry for each.
const NumAuthRoutesForTest = numAuthRoutes

// MaxKeyErrorLogsForTest and MaxLoggedLenForTest are DeleteObjects' bounds
// on its per-key error lines and on what each line carries.
const (
	MaxKeyErrorLogsForTest = maxKeyErrorLogs
	MaxLoggedLenForTest    = maxLoggedLen
)
