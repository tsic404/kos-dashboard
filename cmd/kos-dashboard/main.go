// Command kos-dashboard serves the browser front of the desktop: the
// control-plane API proxy, the session bootstrap, the desktop page, the event
// stream and the window-application host (DASH-C-22, DASH-C-30, KOSD-C-12,
// KOSD-C-63, KOSD-C-73, KOSD-C-75).
//
// The control plane keeps accounts and sessions; this process holds no cluster
// credential and forwards every read and write as the caller's session
// (KOSD-F-9).
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/bff"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("kos-dashboard stopped", "error", err)
		os.Exit(1)
	}
}

// options is the process configuration the command line names.
type options struct {
	address       string
	upstream      string
	secure        bool
	frameOrigins  []string
	consoleOrigin string
	appAssetsRoot string
	allowInsecure bool
}

// parseOptions reads the process configuration. -frame-origin repeats, so every
// origin the operator names is framed and not only the last one (KOSD-F-18).
func parseOptions(args []string) (options, error) {
	parsed := options{}
	flags := flag.NewFlagSet("kos-dashboard", flag.ContinueOnError)
	flags.StringVar(&parsed.address, "address", ":8080", "listen address of the browser front")
	flags.StringVar(&parsed.upstream, "upstream", "http://127.0.0.1:8081", "control-plane API base URL")
	flags.BoolVar(&parsed.secure, "secure-cookies", false,
		"mark the session cookie Secure (set behind TLS)")
	flags.StringVar(&parsed.consoleOrigin, "console-origin", "",
		"origin the console is served from; required, and no application entry may share it (KOSD-F-18)")
	flags.StringVar(&parsed.appAssetsRoot, "app-assets-root", "",
		"directory the verified application packages are unpacked under, as <extension>/<version>/<digest>/<asset> (KOSD-C-73)")
	flags.BoolVar(&parsed.allowInsecure, "allow-insecure-app-entries", false,
		"admit plain-HTTP application entries on a loopback host, which is how a local deployment serves an application (KOSD-C-73)")
	flags.Func("frame-origin", "application origin a window may frame (repeatable)", func(origin string) error {
		if origin == "" {
			return fmt.Errorf("frame-origin names no origin")
		}
		parsed.frameOrigins = append(parsed.frameOrigins, origin)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	return parsed, nil
}

// validate judges what the process cannot serve without. Both checks fail at
// start rather than at the first window: the console origin is the one guard
// keeping an application entry off the console origin (KOSD-F-18), and this
// process is the only one that can sign the asset addresses it serves, because
// the signature uses the session seal key (KOSD-C-73).
func (o options) validate() error {
	if o.consoleOrigin == "" {
		return fmt.Errorf("-console-origin is required: an application entry may never share the console origin (KOSD-F-18)")
	}
	parsed, err := url.Parse(o.consoleOrigin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("-console-origin %q is not an origin", o.consoleOrigin)
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("-console-origin %q is not an origin: it names no path", o.consoleOrigin)
	}
	if o.appAssetsRoot == "" {
		return fmt.Errorf("-app-assets-root is required: a window is opened on a signed asset address this process serves (KOSD-C-73)")
	}
	return nil
}

// run reads the process configuration and serves until it fails.
func run(args []string) error {
	config, err := parseOptions(args)
	if err != nil {
		return err
	}
	if err := config.validate(); err != nil {
		return err
	}
	key, err := sessionKey()
	if err != nil {
		return err
	}
	server, parsed, err := newServer(config, key)
	if err != nil {
		return err
	}
	listener := &http.Server{
		Addr:              config.address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("kos-dashboard listening", "address", config.address, "upstream", parsed.String(),
		"consoleOrigin", config.consoleOrigin, "appAssetsRoot", config.appAssetsRoot)
	return listener.ListenAndServe()
}

// newServer builds the browser front from one parsed configuration. The asset
// root is wired as the store the signed asset addresses are served from
// (KOSD-C-73).
func newServer(config options, key []byte) (*bff.Server, *url.URL, error) {
	parsed, err := url.Parse(config.upstream)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, nil, fmt.Errorf("upstream %q is not a base URL", config.upstream)
	}
	var assets apphost.AssetStore
	if config.appAssetsRoot != "" {
		assets = &apphost.DirAssets{Root: config.appAssetsRoot}
	}
	server, err := bff.New(bff.Config{
		Upstream:             parsed,
		SealKey:              key,
		SecureCookies:        config.secure,
		FrameOrigins:         config.frameOrigins,
		ConsoleOrigin:        config.consoleOrigin,
		AllowInsecureEntries: config.allowInsecure,
		AssetFiles:           assets,
	})
	if err != nil {
		return nil, nil, err
	}
	return server, parsed, nil
}

// sessionKey reads the cookie seal key. Without one the process draws a random
// key, which ends every session when it restarts.
func sessionKey() ([]byte, error) {
	encoded := os.Getenv("KOS_DASHBOARD_SESSION_KEY")
	if encoded == "" {
		slog.Warn("KOS_DASHBOARD_SESSION_KEY is unset; sessions end when this process restarts")
		return nil, nil
	}
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("KOS_DASHBOARD_SESSION_KEY is not base64: %w", err)
	}
	return key, nil
}
