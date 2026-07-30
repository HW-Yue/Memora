package security

import "regexp"

var (
	assignmentSecret = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD))\s*=\s*([^\s,;]+)`)
	bearerSecret     = regexp.MustCompile(`(?i)\bBearer\s+([^\s,;]+)`)
)

func Redact(value string) string {
	value = assignmentSecret.ReplaceAllString(value, `${1}=[REDACTED]`)
	return bearerSecret.ReplaceAllString(value, `Bearer [REDACTED]`)
}
