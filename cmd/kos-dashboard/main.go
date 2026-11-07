// Command kos-dashboard serves the browser front of the desktop: the
// control-plane API proxy, the session bootstrap, the desktop page and the
// event stream (DASH-C-22, KOSD-C-12, KOSD-C-63).
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
}

// parseOptions reads the process configuration. -frame-origin repeats, so every
// origin the operator names is framed and not only the last one (KOSD-F-18).
func parseOptions(args []string) (options, error) {
	parsed := options{}
	flags := flag.NewFlagSet("kos-dashboard", flag.ContinueOnError)
	flags.StringVar(&parsed.address, "address", ":8080", "listen address of the browser front")
	flags.StringVar(&parsed.upstream, "upstream", "http://127.0.0.1:8081", "control-plane API base URL")
	flags.BoolVar(&parsed.secure, "secure-cookies", false, "mark the session cookie Secure (set behind TLS)")
	flags.StringVar(&parsed.consoleOrigin, "console-origin", "", "origin this front is served from; an application entry may never share it (KOSD-F-18)")
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

// run reads the process configuration and serves until it fails.
func run(args []string) error {
	config, err := parseOptions(args)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(config.upstream)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("upstream %q is not a base URL", config.upstream)
	}
	key, err := sessionKey()
	if err != nil {
		return err
	}
	server, err := bff.New(bff.Config{
		Upstream:      parsed,
		SealKey:       key,
		SecureCookies: config.secure,
		FrameOrigins:  config.frameOrigins,
		ConsoleOrigin: config.consoleOrigin,
	})
	if err != nil {
		return err
	}
	listener := &http.Server{
		Addr:              config.address,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("kos-dashboard listening", "address", config.address, "upstream", parsed.String())
	return listener.ListenAndServe()
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
