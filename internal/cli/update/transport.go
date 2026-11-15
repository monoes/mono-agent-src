package update

import "time"

var (
	stallTimeout         = 60 * time.Second
	totalTimeout         = 15 * time.Minute
	apiTimeout           = 30 * time.Second
	maxBinaryBytes int64 = 512 << 20
	maxSmallBytes  int64 = 4 << 20
)
