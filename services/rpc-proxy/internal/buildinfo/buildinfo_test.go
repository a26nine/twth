package buildinfo

import "testing"

func TestCurrentReturnsDevelopmentDefaults(t *testing.T) {
	got := Current()
	want := Info{Version: "dev", Commit: "unknown"}

	if got != want {
		t.Fatalf("Current() = %+v, want %+v", got, want)
	}
}

func TestInfoString(t *testing.T) {
	info := Info{
		Version: "1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}

	if got, want := info.String(), "rpc-proxy version=1.2.3 commit=0123456789abcdef0123456789abcdef01234567"; got != want {
		t.Fatalf("Info.String() = %q, want %q", got, want)
	}
}
