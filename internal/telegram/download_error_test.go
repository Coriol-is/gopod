package telegram

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestDownloadErrorKeepsCauseWithoutBotCredential(t *testing.T) {
	const credential = "123456:download-canary-abc"
	original := &url.Error{Op: "Get", URL: "https://api.telegram.org/file/bot" + credential + "/voice/file.ogg", Err: context.DeadlineExceeded}
	safe := redactDownloadError(original)
	if strings.Contains(safe.Error(), credential) {
		t.Fatal("bot credential leaked")
	}
	if !errors.Is(safe, context.DeadlineExceeded) {
		t.Fatal("lost cancellation cause")
	}
	if !strings.Contains(safe.Error(), "deadline exceeded") {
		t.Fatal("lost failure detail")
	}
}
