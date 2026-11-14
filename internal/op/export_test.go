package op

import "time"

// NumAuthRoutesForTest is the number of AuthRoute values, so that a spec can
// require an entry for each.
const NumAuthRoutesForTest = numAuthRoutes

// MaxKeyErrorLogsForTest and MaxLoggedLenForTest are DeleteObjects' bounds
// on its per-key error lines and on what each line carries.
const (
	MaxKeyErrorLogsForTest = maxKeyErrorLogs
	MaxLoggedLenForTest    = maxLoggedLen
)

// DeleteMarkerVersionForTest is deleteMarkerVersion, so that a spec can give
// it more than one action.
var DeleteMarkerVersionForTest = deleteMarkerVersion

// SkippedUpdateGraceForTest is skippedUpdateGrace, the bound on DeleteObjects'
// OLH updates once its client has gone.
const SkippedUpdateGraceForTest = skippedUpdateGrace

// SetUpdateGraceForTest makes o's OLH updates end d after the request's
// context, in place of skippedUpdateGrace.
func SetUpdateGraceForTest(o *DeleteObjects, d time.Duration) { o.updateGrace = d }

// ParseErrForTest is the error Init kept of parsing the document, nil when
// the document was not parsed or parsed.
func (o *PutObjectRetention) ParseErrForTest() error { return o.parseErr }

// ParseErrForTest is the error Init kept of parsing the document, nil when
// the document was not parsed or parsed.
func (o *PutObjectLegalHold) ParseErrForTest() error { return o.parseErr }
