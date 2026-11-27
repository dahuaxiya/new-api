package onboarding

import (
	_ "embed"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MinPasswordLength = 8
	MaxPasswordLength = 128
)

var (
	ErrPasswordLength = errors.New("Password must contain between 8 and 128 characters.")
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

// ValidatePassword 校验注册密码的 8–128 字符边界和常见密码黑名单。
// 长度按 Unicode 字符计算；空格及原始编码必须原样传给密码哈希器。
// 此策略用于新密码，不应用于已有账号的登录验证。
// @param password 用户输入的密码原文，调用方不得预先去空格或归一化。
// @return 长度、编码或黑名单校验失败的错误；符合策略时返回 nil。
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
