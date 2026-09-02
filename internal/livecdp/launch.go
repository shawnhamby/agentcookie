package livecdp

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FindChrome locates the Google Chrome executable. macOS app bundles first
// (the agentcookie target platform), then PATH. The owned browser must be
// real Chrome -- only enabled Google Chrome is used.
func FindChrome() (string, error) {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
	}
	candidates = append(candidates,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	)
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find Google Chrome; install it or pass --chrome-path")
}

// LaunchOptions configures the owned Chrome launch argv.
type LaunchOptions struct {
	ChromePath        string
	UserDataDir       string
	Port              int
	Headless          bool
	UserAgent         string
	WindowSize        string
	ScreenSize        string
	ScreenColorDepth  int
	ScreenWorkArea    string
	DeviceScaleFactor float64
	ColorProfile      string
	ExtraScreens      []string
	ProxyServer       string
	LeanProfile       bool
}

// OwnedChrome is a Chrome instance agentcookie launched and owns. It runs on
// a DEDICATED user-data-dir (so --remote-debugging-port is honored -- Chrome
// 136+ only blocks the flag on the default profile dir) and a loopback debug
// port, leaving the user's everyday Chrome untouched (no single-instance lock).
type OwnedChrome struct {
	cmd         *exec.Cmd
	Port        int
	Endpoint    string // http://127.0.0.1:<port>
	UserDataDir string
}

// LaunchOwnedChrome starts Chrome on userDataDir with a loopback debug port,
// waits for the CDP endpoint, and returns the handle. chromePath empty ->
// FindChrome. headless uses the new headless mode (full feature parity with
// headed for cookie/context behavior).
//
// userAgent, when non-empty, overrides the browser User-Agent at launch (the
// same real-Chrome UA agent-browser workers use), so an attached agent does not
// leak a "HeadlessChrome" token to bot detectors like DataDome. Automation
// fingerprint flags are always applied to match agent-browser's worker profile.
func LaunchOwnedChrome(ctx context.Context, chromePath, userDataDir string, port int, headless bool, userAgent, windowSize string) (*OwnedChrome, error) {
	return LaunchOwnedChromeWithOptions(ctx, LaunchOptions{
		ChromePath:  chromePath,
		UserDataDir: userDataDir,
		Port:        port,
		Headless:    headless,
		UserAgent:   userAgent,
		WindowSize:  windowSize,
	})
}

// LaunchOwnedChromeWithOptions starts Chrome with the full owned-browser argv.
func LaunchOwnedChromeWithOptions(ctx context.Context, opts LaunchOptions) (*OwnedChrome, error) {
	chromePath := opts.ChromePath
	if chromePath == "" {
		var err error
		if chromePath, err = FindChrome(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(opts.UserDataDir, 0o755); err != nil {
		return nil, fmt.Errorf("livecdp: create user-data-dir %q: %w", opts.UserDataDir, err)
	}

	args := BuildChromeLaunchArgs(opts)
	cmd := exec.Command(chromePath, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("livecdp: launch chrome: %w", err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", opts.Port)
	if err := waitForCDP(ctx, endpoint, 25*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	return &OwnedChrome{cmd: cmd, Port: opts.Port, Endpoint: endpoint, UserDataDir: opts.UserDataDir}, nil
}

// BuildChromeLaunchArgs assembles the owned Chrome argv from LaunchOptions.
func BuildChromeLaunchArgs(opts LaunchOptions) []string {
	args := []string{
		"--user-data-dir=" + opts.UserDataDir,
		fmt.Sprintf("--remote-debugging-port=%d", opts.Port),
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		// Anti-detection: hide the automation flag so navigator.webdriver and the
		// blink automation surface match agent-browser's worker profile.
		"--disable-blink-features=AutomationControlled",
	}
	if opts.WindowSize != "" {
		args = append(args, "--window-size="+opts.WindowSize)
	}
	screenSize := opts.ScreenSize
	if screenSize == "" {
		screenSize = opts.WindowSize
	}
	if screenSize != "" {
		// screen.width/height stay at 800x600 without this; headless=new only
		// honors --window-size for the viewport. Chrome reads screen-info in
		// physical pixels, so scale the logical size by the device factor.
		// Extra displays are sibling {WxH} groups; props stay on the primary.
		screenInfo := "{" + buildScreenInfo(screenSize, opts.DeviceScaleFactor, opts.ScreenColorDepth, opts.ScreenWorkArea) + "}"
		for _, extra := range opts.ExtraScreens {
			screenInfo += "{" + screenInfoFromLogicalSize(extra, opts.DeviceScaleFactor) + "}"
		}
		args = append(args, "--screen-info="+screenInfo)
	}
	if opts.DeviceScaleFactor > 0 {
		args = append(args, fmt.Sprintf("--force-device-scale-factor=%g", opts.DeviceScaleFactor))
	}
	if opts.ColorProfile != "" {
		args = append(args, "--force-color-profile="+opts.ColorProfile)
	}
	if opts.UserAgent != "" {
		// Strips the HeadlessChrome token from both navigator.userAgent and the
		// HTTP User-Agent header (the one server-side bot checks read).
		args = append(args, "--user-agent="+opts.UserAgent)
	}
	// Owned agent Chrome only reads pages/DOM (media is a download); block play()
	// without a user gesture so cross-origin iframe players cannot autoplay.
	// MEI preload/bypass can ignore that policy; headless=new still routes audio
	// to the system device, so mute at launch for deterministic silent runs.
	args = append(args,
		"--autoplay-policy=user-gesture-required",
		"--mute-audio",
		"--disable-features=PreloadMediaEngagementData,MediaEngagementBypassAutoplayPolicies",
	)
	if opts.LeanProfile {
		args = appendLeanProfileArgs(args, opts.Port)
	}
	args = appendProxyServerArgs(args, opts.ProxyServer)
	if opts.Headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")
	return args
}

// buildScreenInfo assembles Chrome's --screen-info value: physical WxH plus
// optional colorDepth and work-area insets (logical top,bottom,left,right
// scaled to physical pixels when the device factor is set).
func buildScreenInfo(logicalSize string, scale float64, colorDepth int, workArea string) string {
	info := screenInfoFromLogicalSize(logicalSize, scale)
	if colorDepth > 0 {
		info += fmt.Sprintf(" colorDepth=%d", colorDepth)
	}
	if insets, ok := parseScreenWorkArea(workArea); ok {
		top := scaleScreenInset(insets[0], scale)
		bottom := scaleScreenInset(insets[1], scale)
		left := scaleScreenInset(insets[2], scale)
		right := scaleScreenInset(insets[3], scale)
		info += fmt.Sprintf(" workAreaTop=%d workAreaBottom=%d workAreaLeft=%d workAreaRight=%d",
			top, bottom, left, right)
	}
	return info
}

func scaleScreenInset(logical int, scale float64) int {
	if scale <= 0 {
		return logical
	}
	return int(math.Round(float64(logical) * scale))
}

func parseScreenWorkArea(workArea string) ([4]int, bool) {
	workArea = strings.TrimSpace(workArea)
	if workArea == "" {
		return [4]int{}, false
	}
	parts := strings.Split(workArea, ",")
	if len(parts) != 4 {
		return [4]int{}, false
	}
	var insets [4]int
	for i, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return [4]int{}, false
		}
		insets[i] = v
	}
	return insets, true
}

// screenInfoFromLogicalSize converts a logical "W,H" size into Chrome's
// physical-pixel "WxH" screen-info form, scaling by the device factor when set.
func screenInfoFromLogicalSize(logicalSize string, scale float64) string {
	parts := strings.SplitN(logicalSize, ",", 2)
	if len(parts) != 2 || scale <= 0 {
		return strings.ReplaceAll(logicalSize, ",", "x")
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil {
		return strings.ReplaceAll(logicalSize, ",", "x")
	}
	return fmt.Sprintf("%dx%d", int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale)))
}

func appendLeanProfileArgs(args []string, port int) []string {
	cacheDir := filepath.Join(os.TempDir(), fmt.Sprintf("agentcookie-cache-%d", port))
	return append(args,
		"--disk-cache-dir="+cacheDir,
		"--disk-cache-size=104857600",
		"--disable-gpu-shader-disk-cache",
		"--disable-background-networking",
	)
}

func appendProxyServerArgs(args []string, proxyURL string) []string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return args
	}
	args = append(args, "--proxy-server="+proxyURL)
	if u, err := url.Parse(proxyURL); err == nil {
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			args = append(args, "--disable-quic")
		}
	}
	return args
}

// RedactProxyURL removes userinfo from a proxy URL for safe logging.
func RedactProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted>"
	}
	if u.User != nil {
		host := u.Host
		return u.Scheme + "://<redacted>@" + host
	}
	return u.String()
}

// Close shuts down the owned Chrome: SIGTERM, then SIGKILL if it lingers.
func (o *OwnedChrome) Close() error {
	if o == nil || o.cmd == nil || o.cmd.Process == nil {
		return nil
	}
	_ = o.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = o.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = o.cmd.Process.Kill()
	}
	return nil
}

// waitForCDP polls the CDP /json/version endpoint until it responds 200 or
// the timeout/ctx elapses.
func waitForCDP(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(endpoint + "/json/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("livecdp: chrome CDP endpoint %s not reachable within %s", endpoint, timeout)
}
