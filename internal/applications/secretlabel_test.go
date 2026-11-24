package applications

import "testing"

// A label is a secret by its words, not by letters inside another word ("Hotpoint" is not an OTP) (R10-10).
func TestSecretLabelNeedsWholeWords(t *testing.T) {
	for _, l := range []string{"OTP", "otp_code", "Your OTP", "2FA code", "cvv", "CVC", "Password", "API key", "One-time code"} {
		if !looksSecret(l, "x") {
			t.Errorf("%q should be a secret label", l)
		}
	}
	for _, l := range []string{"Hotpoint model", "Footprint", "Total compensation", "Years of experience", "Postal code"} {
		if looksSecret(l, "x") {
			t.Errorf("%q is not a secret label", l)
		}
	}
}
