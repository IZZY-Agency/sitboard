package sitboard

import (
	"strings"
	"testing"
)

func TestTextsAreEmbedded(t *testing.T) {
	text := Text()
	if !strings.HasPrefix(text, "MIT License") || !strings.Contains(text, "Copyright (c) 2026 IZZY Agency") {
		t.Errorf("Text does not open with the MIT licence: %.80q", text)
	}
	i := strings.Index(text, "\n# Third-party notices\n")
	if i < 0 || !strings.HasPrefix(text[i+1:], notices) || license+"\n"+notices != text {
		t.Error("Text is not the licence followed by the notices")
	}
}
