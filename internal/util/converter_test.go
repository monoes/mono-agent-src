package util

import "testing"

func TestConvertAbbreviatedNumber(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"42", 42},
		{"3.7", 4}, // rounds
		{"12.5K", 12500},
		{"1.2M", 1200000},
		{"3B", 3000000000},
		{"2k", 2000},
		{"5m", 5000000},
		{"1,234", 1234},
		{"1,234,567", 1234567},
		{" 10K ", 10000},
	}
	for _, c := range cases {
		got, err := ConvertAbbreviatedNumber(c.in)
		if err != nil {
			t.Errorf("ConvertAbbreviatedNumber(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ConvertAbbreviatedNumber(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestConvertAbbreviatedNumber_RefusesWhatIsNotAnInt64(t *testing.T) {
	for _, in := range []string{"NaN", "Inf", "-Inf", "1e30", "1e19", "9999999999999B"} {
		if got, err := ConvertAbbreviatedNumber(in); err == nil {
			t.Errorf("ConvertAbbreviatedNumber(%q) = %d, want an error", in, got)
		}
	}
}

func TestConvertAbbreviatedNumber_Errors(t *testing.T) {
	for _, in := range []string{"", "   ", "abc", "K"} {
		if _, err := ConvertAbbreviatedNumber(in); err == nil {
			t.Errorf("ConvertAbbreviatedNumber(%q) = nil error, want error", in)
		}
	}
}
