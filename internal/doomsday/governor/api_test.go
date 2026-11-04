package governor

import "testing"

func TestLimitsDefaultsNeverRaiseTheCapAndTheSafeMinimumStaysInsideIt(t *testing.T) {
	for _, tc := range []struct{ cap, safe, want int }{{30, 0, 2}, {30, 5, 5}, {1, 0, 1}, {1, 7, 1}, {2, -3, 1}} {
		l := Limits{MaxConcurrent: tc.cap, SafeMin: tc.safe}.WithDefaults()
		if l.SafeMin != tc.want || l.SafeMin > l.MaxConcurrent || l.MaxConcurrent != tc.cap {
			t.Errorf("cap %d safe %d: got safe %d cap %d, want safe %d", tc.cap, tc.safe, l.SafeMin, l.MaxConcurrent, tc.want)
		}
	}
	l := Limits{MaxConcurrent: 4}.WithDefaults()
	if l.QueueMaxRows != DefaultQueueMaxRows || l.QueueMaxBytes != DefaultQueueMaxBytes {
		t.Errorf("queue defaults: %+v", l)
	}
}
