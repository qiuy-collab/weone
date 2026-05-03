package messaging

import "testing"

func newTestHandler() *Handler {
	return &Handler{runtimeName: "companion"}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	if text == "/help" {
		t.Error("help text should include command descriptions")
	}
}

func TestBuildStatus(t *testing.T) {
	h := newTestHandler()
	status := h.buildStatus()
	if status == "" {
		t.Fatal("status is empty")
	}
}
