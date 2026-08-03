package persist

import "errors"

// ErrConflict indicates create would overwrite an existing tenant object.
var ErrConflict = errors.New("conflict")

// ErrNotFound is returned when an object does not exist in the caller's org.
var ErrNotFound = errors.New("not found")

// ErrStaleFence means a job lease was stolen / expired (fencing token mismatch).
var ErrStaleFence = errors.New("stale fence token")
