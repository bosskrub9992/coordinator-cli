package shellquote

import "testing"

func TestQuote(t *testing.T) {
	tests := []struct {
		in      string
		windows bool
		want    string
	}{
		{"/usr/local/bin/coord", false, "/usr/local/bin/coord"},
		{"001-x", false, "001-x"},
		{"", false, "''"},
		{"/a b/coord", false, "'/a b/coord'"},
		{"/it's/coord", false, `'/it'\''s/coord'`},
		{"/x/$HOME/coord", false, "'/x/$HOME/coord'"},
		{`C:\Users\me\bin\coord.exe`, true, "C:/Users/me/bin/coord.exe"},
		{`C:\Program Files\coord\coord.exe`, true, "'C:/Program Files/coord/coord.exe'"},
		{`C:\Users\o'neil\coord.exe`, true, `'C:/Users/o'\''neil/coord.exe'`},
	}
	for _, tt := range tests {
		if got := quote(tt.in, tt.windows); got != tt.want {
			t.Errorf("quote(%q, windows=%v) = %q want %q", tt.in, tt.windows, got, tt.want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got := Join("/a b/coord", "_guard", "001-x"); got != "'/a b/coord' _guard 001-x" {
		t.Fatalf("Join = %q", got)
	}
}
