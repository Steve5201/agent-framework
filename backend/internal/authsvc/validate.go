package authsvc

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Steve5201/agent-backend/internal/errors"
)

// 用户名规则：3~32 位，字母数字下划线。
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

// validateCredentials 校验注册参数。
// 返回 *errors.Error（CodeInvalidArgument），message 明确告知哪项不满足，
// 便于调用方给出具体反馈（不采用模糊的"参数非法"）。
func validateCredentials(username, password string) error {
	if !usernamePattern.MatchString(username) {
		return errors.New(errors.CodeInvalidArgument,
			"用户名须为 3~32 位字母、数字或下划线")
	}
	if !validPassword(password) {
		return errors.New(errors.CodeInvalidArgument,
			"密码须不少于 8 位，且同时包含字母与数字")
	}
	return nil
}

// validPassword 密码强度：长度 >= 8，同时包含字母与数字。
func validPassword(pw string) bool {
	if len(pw) < 8 {
		return false
	}
	var hasLetter, hasDigit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

// normalizeUsername 登录时对用户名做归一化（去空白、转小写），
// 避免"  admin  " 这类输入导致查询失败。
func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
