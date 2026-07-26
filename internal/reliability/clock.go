package reliability

import "time"

// Clock supplies time to components whose state changes with elapsed time.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}
