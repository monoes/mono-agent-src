package machine

import (
	"math"
	"testing"
)

const vmStatSample = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    12118.
Pages active:                                 265828.
Pages inactive:                               265740.
Pages speculative:                               490.
Pages purgeable:                                1095.
`

func TestParseDarwinLoadAvg(t *testing.T) {
	v, err := ParseDarwinLoadAvg([]byte("{ 2.50 3.10 4.00 }\n"))
	if err != nil || v != 2.5 {
		t.Fatalf("%v %v", v, err)
	}
	for _, bad := range []string{"", "{ }", "{ nan 1 1 }", "garbage"} {
		if _, err := ParseDarwinLoadAvg([]byte(bad)); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestParseDarwinMemAvailFrac(t *testing.T) {
	total := uint64(17179869184)
	got, err := ParseDarwinMemAvailFrac([]byte(vmStatSample), total)
	want := float64((12118+265740+490+1095)*16384) / float64(total)
	if err != nil || math.Abs(got-want) > 1e-9 {
		t.Fatalf("%v %v want %v", got, err, want)
	}
	for _, bad := range []string{"", "Pages free: 1.\n", "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n"} {
		if _, err := ParseDarwinMemAvailFrac([]byte(bad), total); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := ParseDarwinMemAvailFrac([]byte(vmStatSample), 0); err == nil {
		t.Fatal("zero total accepted")
	}
}
