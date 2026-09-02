package livecdp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildChromeLaunchArgs_ProxyAndLean(t *testing.T) {
	tests := []struct {
		name      string
		proxy     string
		wantProxy bool
		wantQUIC  bool
		wantLean  bool
	}{
		{name: "lean only", proxy: "", wantLean: true},
		{name: "http proxy disables quic", proxy: "http://proxy.example:8080", wantProxy: true, wantQUIC: true, wantLean: true},
		{name: "https proxy disables quic", proxy: "https://proxy.example:8443", wantProxy: true, wantQUIC: true, wantLean: true},
		{name: "socks proxy no quic flag", proxy: "socks5://127.0.0.1:1080", wantProxy: true, wantQUIC: false, wantLean: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := BuildChromeLaunchArgs(LaunchOptions{
				UserDataDir: "/tmp/ac-profile",
				Port:        9477,
				Headless:    true,
				ProxyServer: test.proxy,
				LeanProfile: test.wantLean,
			})

			if !slices.Contains(args, "--autoplay-policy=user-gesture-required") {
				t.Fatalf("missing --autoplay-policy=user-gesture-required in args=%v", args)
			}

			hasProxy := slices.ContainsFunc(args, func(a string) bool {
				return strings.HasPrefix(a, "--proxy-server=")
			})
			if hasProxy != test.wantProxy {
				t.Fatalf("proxy arg present = %v, want %v; args=%v", hasProxy, test.wantProxy, args)
			}
			if test.wantProxy {
				want := "--proxy-server=" + test.proxy
				if !slices.Contains(args, want) {
					t.Fatalf("missing %q in args=%v", want, args)
				}
			}

			hasQUIC := slices.Contains(args, "--disable-quic")
			if hasQUIC != test.wantQUIC {
				t.Fatalf("--disable-quic present = %v, want %v; args=%v", hasQUIC, test.wantQUIC, args)
			}

			wantCacheDir := "--disk-cache-dir=" + filepath.Join(os.TempDir(), "agentcookie-cache-9477")
			for _, flag := range []string{
				wantCacheDir,
				"--disk-cache-size=104857600",
				"--disable-gpu-shader-disk-cache",
				"--disable-background-networking",
			} {
				if test.wantLean {
					if !slices.Contains(args, flag) {
						t.Fatalf("missing lean flag %q in args=%v", flag, args)
					}
				} else if slices.Contains(args, flag) {
					t.Fatalf("unexpected lean flag %q when LeanProfile=false", flag)
				}
			}
		})
	}
}

func TestRedactProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "no credentials", in: "http://proxy.example:8080", want: "http://proxy.example:8080"},
		{name: "userinfo redacted", in: "http://user:secret@proxy.example:8080", want: "http://<redacted>@proxy.example:8080"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RedactProxyURL(test.in); got != test.want {
				t.Fatalf("RedactProxyURL(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}
