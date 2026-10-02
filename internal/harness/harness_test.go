package harness

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name Name
		want string
	}{
		{Claude, ""},
		{Codex, "not supported yet"},
		{Cursor, "not supported yet"},
		{"gemini", "unknown harness"},
		{"", "unknown harness"},
	}
	for _, tt := range tests {
		err := Check(tt.name)
		if tt.want == "" && err != nil {
			t.Errorf("Check(%q) = %v", tt.name, err)
		}
		if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("Check(%q) = %v, want %q", tt.name, err, tt.want)
		}
	}
}
