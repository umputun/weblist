package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"

	"github.com/fatih/color"
	"github.com/go-pkgz/lgr"
	"github.com/jessevdk/go-flags"

	"github.com/umputun/weblist/server"
)

type options struct {
	Listen  string   `short:"l" long:"listen" env:"LISTEN" default:":8080" description:"address to listen on"`
	Theme   string   `short:"t" long:"theme" env:"THEME" default:"light" description:"theme to use (light or dark)"`
	RootDir string   `short:"r" long:"root" env:"ROOT_DIR" default:"." description:"root directory to serve"`
	Exclude []string `short:"e" long:"exclude" env:"EXCLUDE" description:"files and directories to exclude (can be repeated)"`
	Auth    string   `short:"a" long:"auth" env:"AUTH" description:"password for basic authentication (username is 'weblist')"`

	HideFooter bool `short:"f" long:"hide-footer" env:"HIDE_FOOTER"  description:"hide footer"`
	Version    bool `short:"v" long:"version" env:"VERSION" description:"show version and exit"`
	Dbg        bool `long:"dbg" env:"DEBUG" description:"debug mode"`
}

var opts options

func main() {
	fmt.Printf("weblist %s\n", versionInfo())
	p := flags.NewParser(&opts, flags.PrintErrors|flags.PassDoubleDash|flags.HelpFlag)
	if _, err := p.Parse(); err != nil {
		if !errors.Is(err.(*flags.Error).Type, flags.ErrHelp) {
			fmt.Printf("%v", err)
		}
		os.Exit(1)
	}
	setupLog(opts.Dbg)

	if opts.Version {
		fmt.Printf("weblist %s\n", versionInfo())
		os.Exit(0)
	}

	// validate theme
	if opts.Theme != "light" && opts.Theme != "dark" {
		log.Printf("WARN: invalid theme '%s'. Using 'light' instead.", opts.Theme)
		opts.Theme = "light"
	}

	// get absolute path for root directory
	absRootDir, err := filepath.Abs(opts.RootDir)
	if err != nil {
		log.Fatalf("failed to get absolute path for root directory: %v", err)
	}
	opts.RootDir = absRootDir

	defer func() {
		if x := recover(); x != nil {
			log.Printf("[WARN] run time panic:\n%v", x)
			panic(x)
		}
	}()

	srv := &server.Web{
		Config: server.Config{
			ListenAddr: opts.Listen,
			Theme:      opts.Theme,
			HideFooter: opts.HideFooter,
			RootDir:    opts.RootDir,
			Version:    versionInfo(),
			Exclude:    opts.Exclude,
			Auth:       opts.Auth,
		},
		FS: os.DirFS(opts.RootDir), // create OS filesystem locked to the root directory
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("failed to run server: %v", err) //nolint
	}
}

// showVersionInfo displays the version information from Go's build info
func versionInfo() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		version := info.Main.Version
		if version == "" {
			version = "dev"
		}
		return version
	}
	return "unknown"
}

func setupLog(dbg bool, secrets ...string) {
	logOpts := []lgr.Option{lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	if dbg {
		logOpts = []lgr.Option{lgr.Debug, lgr.CallerFile, lgr.CallerFunc, lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	}

	colorizer := lgr.Mapper{
		ErrorFunc:  func(s string) string { return color.New(color.FgHiRed).Sprint(s) },
		WarnFunc:   func(s string) string { return color.New(color.FgRed).Sprint(s) },
		InfoFunc:   func(s string) string { return color.New(color.FgYellow).Sprint(s) },
		DebugFunc:  func(s string) string { return color.New(color.FgWhite).Sprint(s) },
		CallerFunc: func(s string) string { return color.New(color.FgBlue).Sprint(s) },
		TimeFunc:   func(s string) string { return color.New(color.FgCyan).Sprint(s) },
	}
	logOpts = append(logOpts, lgr.Map(colorizer))

	if len(secrets) > 0 {
		logOpts = append(logOpts, lgr.Secret(secrets...))
	}
	lgr.SetupStdLogger(logOpts...)
	lgr.Setup(logOpts...)
}
