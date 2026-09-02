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
			if !slices.Contains(args, "--mute-audio") {
				t.Fatalf("missing --mute-audio in args=%v", args)
			}
			disableFeatures := slices.DeleteFunc(slices.Clone(args), func(a string) bool {
				return !strings.HasPrefix(a, "--disable-features=")
			})
			if len(disableFeatures) != 1 {
				t.Fatalf("want exactly one --disable-features= arg, got %d in args=%v", len(disableFeatures), args)
			}
			for _, feature := range []string{
				"PreloadMediaEngagementData",
				"MediaEngagementBypassAutoplayPolicies",
			} {
				if !strings.Contains(disableFeatures[0], feature) {
					t.Fatalf("missing %q in %q; args=%v", feature, disableFeatures[0], args)
				}
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

func TestBuildChromeLaunchArgs_ScreenInfoAndDeviceScale(t *testing.T) {
	t.Run("window size emits screen-info", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
			WindowSize:  "3200,1800",
		})
		if !slices.Contains(args, "--window-size=3200,1800") {
			t.Fatalf("missing --window-size in args=%v", args)
		}
		if !slices.Contains(args, "--screen-info={3200x1800}") {
			t.Fatalf("missing --screen-info in args=%v", args)
		}
	})

	t.Run("screen-info scales to physical pixels", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:       "/tmp/ac-profile",
			Port:              9477,
			WindowSize:        "3200,1800",
			DeviceScaleFactor: 1.6,
		})
		if !slices.Contains(args, "--screen-info={5120x2880}") {
			t.Fatalf("missing scaled --screen-info in args=%v", args)
		}
	})

	t.Run("screen-size and window-size independent", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
			WindowSize:  "1600,1000",
			ScreenSize:  "3200,1800",
		})
		if !slices.Contains(args, "--window-size=1600,1000") {
			t.Fatalf("missing --window-size in args=%v", args)
		}
		if !slices.Contains(args, "--screen-info={3200x1800}") {
			t.Fatalf("missing --screen-info from ScreenSize in args=%v", args)
		}
	})

	t.Run("screen-size scales to physical pixels", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:       "/tmp/ac-profile",
			Port:              9477,
			WindowSize:        "1600,1000",
			ScreenSize:        "3200,1800",
			DeviceScaleFactor: 1.6,
		})
		if !slices.Contains(args, "--window-size=1600,1000") {
			t.Fatalf("missing --window-size in args=%v", args)
		}
		if !slices.Contains(args, "--screen-info={5120x2880}") {
			t.Fatalf("missing scaled --screen-info from ScreenSize in args=%v", args)
		}
	})

	t.Run("screen-size alone omits window-size", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
			ScreenSize:  "3200,1800",
		})
		for _, arg := range args {
			if strings.HasPrefix(arg, "--window-size=") {
				t.Fatalf("unexpected %q when WindowSize empty; args=%v", arg, args)
			}
		}
		if !slices.Contains(args, "--screen-info={3200x1800}") {
			t.Fatalf("missing --screen-info in args=%v", args)
		}
	})

	t.Run("empty window size omits screen-info", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
		})
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screen-info=") {
				t.Fatalf("unexpected %q when WindowSize empty; args=%v", arg, args)
			}
		}
	})

	t.Run("device scale factor only when set", func(t *testing.T) {
		unset := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
		})
		for _, arg := range unset {
			if strings.HasPrefix(arg, "--force-device-scale-factor=") {
				t.Fatalf("unexpected %q when DeviceScaleFactor unset; args=%v", arg, unset)
			}
		}

		set := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:       "/tmp/ac-profile",
			Port:              9477,
			DeviceScaleFactor: 1.6,
		})
		if !slices.Contains(set, "--force-device-scale-factor=1.6") {
			t.Fatalf("missing --force-device-scale-factor=1.6 in args=%v", set)
		}
	})

	t.Run("color profile only when set", func(t *testing.T) {
		unset := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir: "/tmp/ac-profile",
			Port:        9477,
		})
		for _, arg := range unset {
			if strings.HasPrefix(arg, "--force-color-profile=") {
				t.Fatalf("unexpected %q when ColorProfile unset; args=%v", arg, unset)
			}
		}

		set := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:  "/tmp/ac-profile",
			Port:         9477,
			ColorProfile: "hdr10",
		})
		if !slices.Contains(set, "--force-color-profile=hdr10") {
			t.Fatalf("missing --force-color-profile=hdr10 in args=%v", set)
		}
	})

	t.Run("extra screens appended in order and scaled", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:       "/tmp/ac-profile",
			Port:              9477,
			WindowSize:        "3200,1800",
			DeviceScaleFactor: 1.6,
			ScreenColorDepth:  30,
			ExtraScreens:      []string{"4000,2250", "3200,1800"},
		})
		want := "--screen-info={5120x2880 colorDepth=30}{6400x3600}{5120x2880}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
	})

	t.Run("empty extra screens leave primary only", func(t *testing.T) {
		args := BuildChromeLaunchArgs(LaunchOptions{
			UserDataDir:  "/tmp/ac-profile",
			Port:         9477,
			WindowSize:   "3200,1800",
			ExtraScreens: nil,
		})
		if !slices.Contains(args, "--screen-info={3200x1800}") {
			t.Fatalf("missing primary-only --screen-info in args=%v", args)
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "--screen-info=") && strings.Count(arg, "{") != 1 {
				t.Fatalf("unexpected multi-screen --screen-info when ExtraScreens empty: %q", arg)
			}
		}
	})
}

func TestBuildChromeLaunchArgs_ScreenColorDepthAndWorkArea(t *testing.T) {
	base := LaunchOptions{
		UserDataDir: "/tmp/ac-profile",
		Port:        9477,
		WindowSize:  "3200,1800",
	}

	t.Run("color depth omitted when zero", func(t *testing.T) {
		args := BuildChromeLaunchArgs(base)
		want := "--screen-info={3200x1800}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
	})

	t.Run("color depth emitted when set", func(t *testing.T) {
		opts := base
		opts.ScreenColorDepth = 30
		args := BuildChromeLaunchArgs(opts)
		want := "--screen-info={3200x1800 colorDepth=30}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
	})

	t.Run("work area emitted when set", func(t *testing.T) {
		opts := base
		opts.ScreenWorkArea = "30,88,0,0"
		args := BuildChromeLaunchArgs(opts)
		want := "--screen-info={3200x1800 workAreaTop=30 workAreaBottom=88 workAreaLeft=0 workAreaRight=0}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
	})

	t.Run("color depth and work area scale with device factor", func(t *testing.T) {
		opts := base
		opts.ScreenColorDepth = 30
		opts.ScreenWorkArea = "30,88,0,0"
		opts.DeviceScaleFactor = 1.6
		args := BuildChromeLaunchArgs(opts)
		want := "--screen-info={5120x2880 colorDepth=30 workAreaTop=48 workAreaBottom=141 workAreaLeft=0 workAreaRight=0}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
	})

	t.Run("malformed work area ignored", func(t *testing.T) {
		opts := base
		opts.ScreenColorDepth = 30
		opts.ScreenWorkArea = "30,88,0"
		args := BuildChromeLaunchArgs(opts)
		want := "--screen-info={3200x1800 colorDepth=30}"
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in args=%v", want, args)
		}
		for _, arg := range args {
			if strings.Contains(arg, "workAreaTop=") {
				t.Fatalf("unexpected work area in malformed case: %q", arg)
			}
		}
	})

	t.Run("invalid work area token ignored without panic", func(t *testing.T) {
		opts := base
		opts.ScreenWorkArea = "30,foo,0,0"
		_ = BuildChromeLaunchArgs(opts)
	})
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
