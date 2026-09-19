package scanner

import "time"

type Result struct {
	PathIndex  uint32
	StatusCode uint16
	Size       int64
	Duration   time.Duration
	Err        error
}