package onboarding

import (
	_ "embed"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MinPasswordLength = 15
	MaxPasswordLength = 128
)

var (
	ErrPasswordLength = errors.New("Password must contain between 15 and 128 characters.")
	ErrCommonPassword = errors.New("This password is too common. Choose a different password.")
)

// commonPasswordData is a fixed, public common-password dataset, never customer
// data. Its provenance and checksum are recorded in common-passwords.SOURCE.
// ASVS 5.0.0 6.2.4 requires 3000 common entries that meet this flow's length
// policy; filtering a top-3000 list afterward would leave too few entries.
//
//go:embed common-passwords.txt
var commonPasswordData string

var commonPasswords = loadCommonPasswords()

// loadCommonPasswords builds an immutable local lookup once. Embedding avoids
// network checks, runtime file dependencies, and disclosure of user passwords.
func loadCommonPasswords() map[string]struct{} {
	passwords := make(map[string]struct{}, 3000)
	for password := range strings.SplitSeq(commonPasswordData, "\n") {
		if password != "" {
			// Remove only the record delimiter via SplitSeq. Spaces, case and
			// Unicode composition are significant parts of password identity.
			passwords[password] = struct{}{}
		}
	}
	return passwords
}

// ValidatePassword checks new onboarding passwords without changing their
// original bytes. Existing logins must not use this new-password policy.
// All valid Unicode characters, including whitespace, are accepted subject to
// the 15–128 character limit and exact-match local common-password blocklist.
// The caller must pass the unchanged password to the account password hasher.
func ValidatePassword(password string) error {
	// Reject oversized input before counting runes; every valid Unicode rune
	// occupies at most four UTF-8 bytes. No password content enters errors/logs.
	if len(password) > MaxPasswordLength*utf8.UTFMax || !utf8.ValidString(password) {
		return ErrPasswordLength
	}
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength || length > MaxPasswordLength {
		return ErrPasswordLength
	}
	if _, blocked := commonPasswords[password]; blocked {
		return ErrCommonPassword
	}
	return nil
}
