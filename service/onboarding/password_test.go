package onboarding

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestValidatePasswordUnicodeBounds checks character-based limits without
// trimming, case folding, normalization, or additional composition rules.
func TestValidatePasswordUnicodeBounds(t *testing.T) {
	for _, test := range []struct {
		name     string
		password string
		want     error
	}{
		{name: "empty", password: "", want: ErrPasswordLength},
		{name: "14_characters", password: strings.Repeat("界", 14), want: ErrPasswordLength},
		{name: "15_characters", password: strings.Repeat("界", 15)},
		{name: "128_characters_512_bytes", password: strings.Repeat("🪷", 128)},
		{name: "129_characters", password: strings.Repeat("界", 129), want: ErrPasswordLength},
		{name: "oversized_bytes", password: strings.Repeat("🪷", 129), want: ErrPasswordLength},
		{name: "invalid_utf8", password: strings.Repeat("a", 15) + "\xff", want: ErrPasswordLength},
		{name: "surrounding_spaces_count", password: " abcdefghijklm "},
		{name: "unicode_composition_unchanged", password: strings.Repeat("e\u0301", 8)},
		{name: "uppercase_permitted", password: "HARBOR GLASS SEVEN SUNRISE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePassword(test.password); !errors.Is(err, test.want) {
				t.Fatalf("password policy returned %v; expected %v", err, test.want)
			}
		})
	}
}

// TestValidatePasswordCommonEntries checks both ends of the selected source
// range, including a common password that already meets the length policy.
func TestValidatePasswordCommonEntries(t *testing.T) {
	for _, password := range []string{"demon1q2w3e4r5t", "YfDbUfNjH10305070", "benoit39123772005"} {
		if err := ValidatePassword(password); !errors.Is(err, ErrCommonPassword) {
			t.Fatalf("public dictionary entry was not rejected: %v", err)
		}
	}
}

// TestEmbeddedCommonPasswordCoverage guards against accidentally shipping a
// truncated or normalized dictionary that no longer supplies 3000 eligible
// distinct entries as required by ASVS 5.0.0 6.2.4.
func TestEmbeddedCommonPasswordCoverage(t *testing.T) {
	digest := sha256.Sum256([]byte(commonPasswordData))
	if hex.EncodeToString(digest[:]) != "6eb5434931cf41e27aa62ebb684b682b0b0b7a0f014d954c00e00c3802906f92" {
		t.Fatal("embedded dictionary differs from the documented pinned snapshot")
	}
	if len(commonPasswords) != 3000 {
		t.Fatalf("dictionary contains %d unique entries; expected 3000", len(commonPasswords))
	}
	for password := range commonPasswords {
		if !utf8.ValidString(password) || utf8.RuneCountInString(password) < MinPasswordLength || utf8.RuneCountInString(password) > MaxPasswordLength {
			t.Fatal("dictionary contains an entry outside the supported password policy")
		}
		if err := ValidatePassword(password); !errors.Is(err, ErrCommonPassword) {
			t.Fatalf("dictionary entry was not rejected as common: %v", err)
		}
	}
}
