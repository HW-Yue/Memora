package security_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/security"
)

func TestRedactRemovesCommonSecretsWithoutEchoingValues(t *testing.T) {
	t.Parallel()

	input := "OPENAI_API_KEY=sk-secret-value Authorization: Bearer bearer-secret token=plain-secret"
	got := security.Redact(input)
	for _, secret := range []string{"sk-secret-value", "bearer-secret", "plain-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Redact() leaked %q in %q", secret, got)
		}
	}
	if strings.Count(got, "[REDACTED]") != 3 {
		t.Fatalf("Redact() = %q", got)
	}
}
