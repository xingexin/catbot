package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/infra/agent/modelapi"
	"strings"
	"testing"
)

func TestModelCallErrorsExcludeProviderContent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("credential SECRET in URL: %w", context.Canceled), "canceled"},
		{fmt.Errorf("private PROMPT: %w", context.DeadlineExceeded), "timed out"},
		{&modelapi.HTTPError{Status: 403}, "HTTP 403"},
		{errors.New("SECRET private PROMPT response"), "failed"},
	} {
		got := safeModelCallError(tc.err)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "SECRET") || strings.Contains(got, "PROMPT") {
			t.Fatalf("unsafe or inaccurate error %q", got)
		}
	}
}
