package state

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"

	"go.k6.io/k6/lib"
	"go.k6.io/k6/ui/console"
)

const defaultConfigFileName = "config.json"

// GlobalState contains the GlobalFlags and accessors for most of the global
// process-external state like CLI arguments, env vars, standard input, output
// and error, etc. In practice, most of it is normally accessed through the `os`
// package from the Go stdlib.
//
// We group them here so we can prevent direct access to them from the rest of
// the k6 codebase. This gives us the ability to mock them and have robust and
// easy-to-write integration-like tests to check the k6 end-to-end behavior in
// any simulated conditions.
//
// `newGlobalState()` returns a globalState object with the real `os`
// parameters, while `newGlobalTestState()` can be used in tests to create
// simulated environments.
type GlobalState struct {
	Ctx                 context.Context
	FS                  afero.Fs
	Getwd               func() (string, error)
	CmdArgs             []string
	Env                 map[string]string
	DefaultFlags, Flags GlobalFlags
	Console             *console.Console
	OSExit              func(int)
	SignalNotify        func(chan<- os.Signal, ...os.Signal)
	SignalStop          func(chan<- os.Signal)
	Logger              *logrus.Logger
}

// NewGlobalState returns a new GlobalState with the given ctx.
// Ideally, this should be the only function in the whole codebase where we use
// global variables and functions from the os package. Anywhere else, things
// like os.Stdout, os.Stderr, os.Stdin, os.Getenv(), etc. should be removed and
// the respective properties of globalState used instead.
func NewGlobalState(ctx context.Context) *GlobalState {
	var logger *logrus.Logger
	confDir, err := os.UserConfigDir()
	if err != nil {
		// The logger is initialized in the Console constructor, so defer
		// logging of this error.
		defer func() {
			logger.WithError(err).Warn("could not get config directory")
		}()
		confDir = ".config"
	}

	env := lib.BuildEnvMap(os.Environ())
	defaultFlags := GetDefaultFlags(confDir)
	flags := getFlags(defaultFlags, env)

	signalNotify := signal.Notify
	signalStop := signal.Stop

	cons := console.New(os.Stdout, os.Stderr, os.Stdin,
		!flags.NoColor, env["TERM"], signalNotify, signalStop)
	logger = cons.GetLogger()

	return &GlobalState{
		Ctx:          ctx,
		FS:           afero.NewOsFs(),
		Getwd:        os.Getwd,
		CmdArgs:      os.Args,
		Env:          env,
		DefaultFlags: defaultFlags,
		Flags:        flags,
		Console:      cons,
		OSExit:       os.Exit,
		SignalNotify: signalNotify,
		SignalStop:   signalStop,
		Logger:       logger,
	}
}

// GlobalFlags contains global config values that apply for all k6 sub-commands.
type GlobalFlags struct {
	ConfigFilePath string
	Quiet          bool
	NoColor        bool
	Address        string
	LogOutput      string
	LogFormat      string
	Verbose        bool
}

// GetDefaultFlags returns the default global flags.
func GetDefaultFlags(homeDir string) GlobalFlags {
	return GlobalFlags{
		Address:        "localhost:6565",
		ConfigFilePath: filepath.Join(homeDir, "loadimpact", "k6", defaultConfigFileName),
		LogOutput:      "stderr",
	}
}

func getFlags(defaultFlags GlobalFlags, env map[string]string) GlobalFlags {
	result := defaultFlags

	// TODO: add env vars for the rest of the values (after adjusting
	// rootCmdPersistentFlagSet(), of course)

	if val, ok := env["K6_CONFIG"]; ok {
		result.ConfigFilePath = val
	}
	if val, ok := env["K6_LOG_OUTPUT"]; ok {
		result.LogOutput = val
	}
	if val, ok := env["K6_LOG_FORMAT"]; ok {
		result.LogFormat = val
	}
	if env["K6_NO_COLOR"] != "" {
		result.NoColor = true
	}
	// Support https://no-color.org/, even an empty value should disable the
	// color output from k6.
	if _, ok := env["NO_COLOR"]; ok {
		result.NoColor = true
	}
	return result
}
