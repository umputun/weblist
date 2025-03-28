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
	Listen                   string   `short:"l" long:"listen" env:"LISTEN" default:":8080" description:"address to listen on"`
	Theme                    string   `short:"t" long:"theme" env:"THEME" default:"light" description:"theme to use (light or dark)"`
	RootDir                  string   `short:"r" long:"root" env:"ROOT_DIR" default:"." description:"root directory to serve"`
	Exclude                  []string `short:"e" long:"exclude" env:"EXCLUDE" description:"files and directories to exclude (can be repeated)"`
	Auth                     string   `short:"a" long:"auth" env:"AUTH" description:"password for basic auth (username is 'weblist')"`
	Title                    string   `long:"title" env:"TITLE" description:"custom title for the site (used in browser title and home)"`
	EnableSyntaxHighlighting bool     `long:"syntax-highlight" env:"SYNTAX_HIGHLIGHT" description:"enable syntax highlighting for code files"`

	SFTP struct {
		Enabled    bool   `long:"enabled" env:"ENABLED" description:"enable SFTP server"`
		User       string `long:"user" env:"USER" description:"username for SFTP access"`
		Address    string `long:"address" env:"ADDRESS" default:":2022" description:"address to listen for SFTP connections"`
		KeyFile    string `long:"key" env:"KEY" default:"weblist_rsa" description:"SSH private key file path"`
		Authorized string `long:"authorized" env:"AUTHORIZED" description:"public key authentication file path"`
	} `group:"SFTP options" namespace:"sftp" env-namespace:"SFTP"`

	Branding struct {
		Name  string `long:"name" env:"NAME" description:"company or organization name to display in navbar"`
		Color string `long:"color" env:"COLOR" description:"color for navbar and footer (e.g. #3498db or 3498db)"`
	} `group:"Branding options" namespace:"brand" env-namespace:"BRAND"`

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
		fmt.Printf("version: %s\n", versionInfo())
		os.Exit(0)
	}

	// validate theme
	if opts.Theme != "light" && opts.Theme != "dark" {
		log.Printf("WARN: invalid theme '%s'. Using 'light' instead.", opts.Theme)
		opts.Theme = "light"
	}

	defer func() {
		if x := recover(); x != nil {
			log.Printf("[WARN] run time panic:\n%v", x)
			panic(x)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()
	if err := runServer(ctx, &opts); err != nil {
		log.Printf("[FATAL] run server error: %v", err)
	}
}

func runServer(ctx context.Context, opts *options) error {
	// get the absolute path for root directory
	absRootDir, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for root directory: %w", err)
	}
	opts.RootDir = absRootDir

	// create OS filesystem locked to the root directory
	fs := os.DirFS(opts.RootDir)

	// prepare common configuration
	config := server.Config{
		ListenAddr:               opts.Listen,
		Theme:                    opts.Theme,
		HideFooter:               opts.HideFooter,
		RootDir:                  opts.RootDir,
		EnableSyntaxHighlighting: opts.EnableSyntaxHighlighting,
		Version:                  versionInfo(),
		Exclude:                  opts.Exclude,
		Auth:                     opts.Auth,
		Title:                    opts.Title,
		SFTPUser:                 opts.SFTP.User,
		SFTPAddress:              opts.SFTP.Address,
		SFTPKeyFile:              opts.SFTP.KeyFile,
		SFTPAuthorized:           opts.SFTP.Authorized,
		BrandName:                opts.Branding.Name,
		BrandColor:               opts.Branding.Color,
	}

	// create HTTP server
	srv := &server.Web{
		Config: config,
		FS:     fs,
	}

	// create error channel for goroutines
	errCh := make(chan error, 2)

	// start HTTP server in a goroutine
	go func() {
		if err := srv.Run(ctx); err != nil {
			errCh <- fmt.Errorf("HTTP server failed: %w", err)
		}
	}()

	// if SFTP is enabled, start SFTP server
	if opts.SFTP.Enabled && opts.SFTP.User != "" {
		// for SFTP, either a password or an authorized_keys file must be provided
		if opts.Auth == "" && opts.SFTP.Authorized == "" {
			return fmt.Errorf("either password (-a/--auth) or authorized keys file (--sftp-authorized) is required for SFTP server")
		}

		sftpSrv := &server.SFTP{
			Config: config,
			FS:     fs,
		}

		go func() {
			if opts.SFTP.Authorized != "" {
				log.Printf("[INFO] starting SFTP server on %s with username %s (public key authentication enabled)", opts.SFTP.Address, opts.SFTP.User)
			} else {
				log.Printf("[INFO] starting SFTP server on %s with username %s (password authentication enabled)", opts.SFTP.Address, opts.SFTP.User)
			}
			if err := sftpSrv.Run(ctx); err != nil {
				errCh <- fmt.Errorf("SFTP server failed: %w", err)
			}
		}()
	}

	// wait for any error or context cancellation
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
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
