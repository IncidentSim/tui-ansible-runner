package remote

import "testing"

func TestShellQuote(t *testing.T) {
	t.Parallel()
	got := shellQuote("a'b c")
	want := `'a'"'"'b c'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}
