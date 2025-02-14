package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	stdlog "log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.k6.io/k6/errext"
	"go.k6.io/k6/lib/consts"
	"go.k6.io/k6/log"
	"go.k6.io/k6/ui/console"
)

const (
	defaultConfigFileName   = "config.json"
	waitRemoteLoggerTimeout = time.Second * 5
)

// globalFlags contains global config values that apply for all k6 sub-commands.
type globalFlags struct {
	configFilePath string
	quiet          bool
	noColor        bool
	address        string
	logOutput      string
	logFormat      string
	verbose        bool
}

// globalState contains the globalFlags and accessors for most of the global
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
type globalState struct {
	ctx context.Context

	fs      afero.Fs
	getwd   func() (string, error)
	args    []string
	envVars map[string]string

	defaultFlags, flags globalFlags

	console *console.Console

	osExit       func(int)
	signalNotify func(chan<- os.Signal, ...os.Signal)
	signalStop   func(chan<- os.Signal)

	logger *logrus.Logger
}

// Ideally, this should be the only function in the whole codebase where we use
// global variables and functions from the os package. Anywhere else, things
// like os.Stdout, os.Stderr, os.Stdin, os.Getenv(), etc. should be removed and
// the respective properties of globalState used instead.
func newGlobalState(ctx context.Context) *globalState {
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

	env := buildEnvMap(os.Environ())
	defaultFlags := getDefaultFlags(confDir)
	flags := getFlags(defaultFlags, env)

	signalNotify := signal.Notify
	signalStop := signal.Stop

	cons := console.New(
		os.Stdout, os.Stderr, os.Stdin,
		!flags.noColor, env["TERM"], signalNotify, signalStop)
	logger = cons.GetLogger()

	return &globalState{
		ctx:          ctx,
		fs:           afero.NewOsFs(),
		getwd:        os.Getwd,
		args:         append(make([]string, 0, len(os.Args)), os.Args...), // copy
		envVars:      env,
		defaultFlags: defaultFlags,
		flags:        flags,
		console:      cons,
		osExit:       os.Exit,
		signalNotify: signal.Notify,
		signalStop:   signal.Stop,
		logger:       logger,
	}
}

func getDefaultFlags(homeFolder string) globalFlags {
	return globalFlags{
		address:        "localhost:6565",
		configFilePath: filepath.Join(homeFolder, "loadimpact", "k6", defaultConfigFileName),
		logOutput:      "stderr",
	}
}

func getFlags(defaultFlags globalFlags, env map[string]string) globalFlags {
	result := defaultFlags

	// TODO: add env vars for the rest of the values (after adjusting
	// rootCmdPersistentFlagSet(), of course)

	if val, ok := env["K6_CONFIG"]; ok {
		result.configFilePath = val
	}
	if val, ok := env["K6_LOG_OUTPUT"]; ok {
		result.logOutput = val
	}
	if val, ok := env["K6_LOG_FORMAT"]; ok {
		result.logFormat = val
	}
	if env["K6_NO_COLOR"] != "" {
		result.noColor = true
	}
	// Support https://no-color.org/, even an empty value should disable the
	// color output from k6.
	if _, ok := env["NO_COLOR"]; ok {
		result.noColor = true
	}
	return result
}

func parseEnvKeyValue(kv string) (string, string) {
	if idx := strings.IndexRune(kv, '='); idx != -1 {
		return kv[:idx], kv[idx+1:]
	}
	return kv, ""
}

func buildEnvMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v := parseEnvKeyValue(kv)
		env[k] = v
	}
	return env
}

// This is to keep all fields needed for the main/root k6 command
type rootCommand struct {
	globalState *globalState

	cmd            *cobra.Command
	loggerStopped  <-chan struct{}
	loggerIsRemote bool
}

func newRootCommand(gs *globalState) *rootCommand {
	c := &rootCommand{
		globalState: gs,
	}
	// the base command when called without any subcommands.
	rootCmd := &cobra.Command{
		Use:               "k6",
		Short:             "a next-generation load generator",
		Long:              "\n" + gs.console.Banner(),
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: c.persistentPreRunE,
	}

	rootCmd.PersistentFlags().AddFlagSet(rootCmdPersistentFlagSet(gs))
	rootCmd.SetArgs(gs.args[1:])
	rootCmd.SetOut(gs.console.Stdout)
	rootCmd.SetErr(gs.console.Stderr) // TODO: use gs.logger.WriterLevel(logrus.ErrorLevel)?
	rootCmd.SetIn(gs.console.Stdin)

	subCommands := []func(*globalState) *cobra.Command{
		getCmdArchive, getCmdCloud, getCmdConvert, getCmdInspect,
		getCmdLogin, getCmdPause, getCmdResume, getCmdScale, getCmdRun,
		getCmdStats, getCmdStatus, getCmdVersion,
	}

	for _, sc := range subCommands {
		rootCmd.AddCommand(sc(gs))
	}

	c.cmd = rootCmd
	return c
}

func (c *rootCommand) persistentPreRunE(cmd *cobra.Command, args []string) error {
	var err error

	c.loggerStopped, err = c.setupLoggers()
	if err != nil {
		return err
	}
	select {
	case <-c.loggerStopped:
	default:
		c.loggerIsRemote = true
	}

	stdlog.SetOutput(c.globalState.logger.Writer())
	c.globalState.logger.Debugf("k6 version: v%s", consts.FullVersion())
	return nil
}

func (c *rootCommand) execute() {
	ctx, cancel := context.WithCancel(c.globalState.ctx)
	defer cancel()
	c.globalState.ctx = ctx

	err := c.cmd.Execute()
	if err == nil {
		cancel()
		c.waitRemoteLogger()
		// TODO: explicitly call c.globalState.osExit(0), for simpler tests and clarity?
		return
	}

	exitCode := -1
	var ecerr errext.HasExitCode
	if errors.As(err, &ecerr) {
		exitCode = int(ecerr.ExitCode())
	}

	errText := err.Error()
	var xerr errext.Exception
	if errors.As(err, &xerr) {
		errText = xerr.StackTrace()
	}

	fields := logrus.Fields{}
	var herr errext.HasHint
	if errors.As(err, &herr) {
		fields["hint"] = herr.Hint()
	}

	c.globalState.logger.WithFields(fields).Error(errText)
	if c.loggerIsRemote {
		c.globalState.logger.WithFields(fields).Error(errText)
		cancel()
		c.waitRemoteLogger()
	}

	c.globalState.osExit(exitCode)
}

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	gs := newGlobalState(context.Background())

	newRootCommand(gs).execute()
}

func (c *rootCommand) waitRemoteLogger() {
	if c.loggerIsRemote {
		select {
		case <-c.loggerStopped:
		case <-time.After(waitRemoteLoggerTimeout):
			c.globalState.logger.Errorf("Remote logger didn't stop in %s", waitRemoteLoggerTimeout)
		}
	}
}

func rootCmdPersistentFlagSet(gs *globalState) *pflag.FlagSet {
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	// TODO: refactor this config, the default value management with pflag is
	// simply terrible... :/
	//
	// We need to use `gs.flags.<value>` both as the destination and as
	// the value here, since the config values could have already been set by
	// their respective environment variables. However, we then also have to
	// explicitly set the DefValue to the respective default value from
	// `gs.defaultFlags.<value>`, so that the `k6 --help` message is
	// not messed up...

	flags.StringVar(&gs.flags.logOutput, "log-output", gs.flags.logOutput,
		"change the output for k6 logs, possible values are stderr,stdout,none,loki[=host:port],file[=./path.fileformat]")
	flags.Lookup("log-output").DefValue = gs.defaultFlags.logOutput

	flags.StringVar(&gs.flags.logFormat, "logformat", gs.flags.logFormat, "log output format")
	oldLogFormat := flags.Lookup("logformat")
	oldLogFormat.Hidden = true
	oldLogFormat.Deprecated = "log-format"
	oldLogFormat.DefValue = gs.defaultFlags.logFormat
	flags.StringVar(&gs.flags.logFormat, "log-format", gs.flags.logFormat, "log output format")
	flags.Lookup("log-format").DefValue = gs.defaultFlags.logFormat

	flags.StringVarP(&gs.flags.configFilePath, "config", "c", gs.flags.configFilePath, "JSON config file")
	// And we also need to explicitly set the default value for the usage message here, so things
	// like `K6_CONFIG="blah" k6 run -h` don't produce a weird usage message
	flags.Lookup("config").DefValue = gs.defaultFlags.configFilePath
	must(cobra.MarkFlagFilename(flags, "config"))

	flags.BoolVar(&gs.flags.noColor, "no-color", gs.flags.noColor, "disable colored output")
	flags.Lookup("no-color").DefValue = strconv.FormatBool(gs.defaultFlags.noColor)

	// TODO: support configuring these through environment variables as well?
	// either with croconf or through the hack above...
	flags.BoolVarP(&gs.flags.verbose, "verbose", "v", gs.defaultFlags.verbose, "enable verbose logging")
	flags.BoolVarP(&gs.flags.quiet, "quiet", "q", gs.defaultFlags.quiet, "disable progress updates")
	flags.StringVarP(&gs.flags.address, "address", "a", gs.defaultFlags.address, "address for the REST API server")

	return flags
}

// RawFormatter it does nothing with the message just prints it
type RawFormatter struct{}

// Format renders a single log entry
func (f RawFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	return append([]byte(entry.Message), '\n'), nil
}

// The returned channel will be closed when the logger has finished flushing and pushing logs after
// the provided context is closed. It is closed if the logger isn't buffering and sending messages
// Asynchronously
func (c *rootCommand) setupLoggers() (<-chan struct{}, error) {
	ch := make(chan struct{})
	close(ch)

	if c.globalState.flags.verbose {
		c.globalState.logger.SetLevel(logrus.DebugLevel)
	}

	switch line := c.globalState.flags.logOutput; {
	case line == "stderr":
		c.globalState.logger.SetOutput(c.globalState.console.Stderr)
	case line == "stdout":
		c.globalState.logger.SetOutput(c.globalState.console.Stdout)
	case line == "none":
		c.globalState.logger.SetOutput(ioutil.Discard)

	case strings.HasPrefix(line, "loki"):
		ch = make(chan struct{}) // TODO: refactor, get it from the constructor
		hook, err := log.LokiFromConfigLine(c.globalState.ctx, c.globalState.logger, line, ch)
		if err != nil {
			return nil, err
		}
		c.globalState.logger.AddHook(hook)
		c.globalState.logger.SetOutput(ioutil.Discard) // don't output to anywhere else
		c.globalState.flags.logFormat = "raw"

	case strings.HasPrefix(line, "file"):
		ch = make(chan struct{}) // TODO: refactor, get it from the constructor
		hook, err := log.FileHookFromConfigLine(
			c.globalState.ctx, c.globalState.fs, c.globalState.getwd,
			c.globalState.logger, line, ch,
		)
		if err != nil {
			return nil, err
		}

		c.globalState.logger.AddHook(hook)
		c.globalState.logger.SetOutput(ioutil.Discard)

	default:
		return nil, fmt.Errorf("unsupported log output '%s'", line)
	}

	switch c.globalState.flags.logFormat {
	case "raw":
		c.globalState.logger.SetFormatter(&RawFormatter{})
		c.globalState.logger.Debug("Logger format: RAW")
	case "json":
		c.globalState.logger.SetFormatter(&logrus.JSONFormatter{})
		c.globalState.logger.Debug("Logger format: JSON")
	default:
		c.globalState.logger.Debug("Logger format: TEXT")
	}
	return ch, nil
}
