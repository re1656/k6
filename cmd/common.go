package cmd

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/guregu/null.v3"

	"go.k6.io/k6/errext/exitcodes"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/types"
	"go.k6.io/k6/output"
)

// Panic if the given error is not nil.
func must(err error) {
	if err != nil {
		panic(err)
	}
}

// TODO: refactor the CLI config so these functions aren't needed - they
// can mask errors by failing only at runtime, not at compile time
func getNullBool(flags *pflag.FlagSet, key string) null.Bool {
	v, err := flags.GetBool(key)
	if err != nil {
		panic(err)
	}
	return null.NewBool(v, flags.Changed(key))
}

func getNullInt64(flags *pflag.FlagSet, key string) null.Int {
	v, err := flags.GetInt64(key)
	if err != nil {
		panic(err)
	}
	return null.NewInt(v, flags.Changed(key))
}

func getNullDuration(flags *pflag.FlagSet, key string) types.NullDuration {
	// TODO: use types.ParseExtendedDuration? not sure we should support
	// unitless durations (i.e. milliseconds) here...
	v, err := flags.GetDuration(key)
	if err != nil {
		panic(err)
	}
	return types.NullDuration{Duration: types.Duration(v), Valid: flags.Changed(key)}
}

func getNullString(flags *pflag.FlagSet, key string) null.String {
	v, err := flags.GetString(key)
	if err != nil {
		panic(err)
	}
	return null.NewString(v, flags.Changed(key))
}

func exactArgsWithMsg(n int, msg string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("accepts %d arg(s), received %d: %s", n, len(args), msg)
		}
		return nil
	}
}

// Trap Interrupts, SIGINTs and SIGTERMs and call the given.
func handleTestAbortSignals(gs *globalState, gracefulStopHandler, onHardStop func(os.Signal)) (stop func()) {
	sigC := make(chan os.Signal, 2)
	done := make(chan struct{})
	gs.signalNotify(sigC, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigC:
			gracefulStopHandler(sig)
		case <-done:
			return
		}

		select {
		case sig := <-sigC:
			if onHardStop != nil {
				onHardStop(sig)
			}
			// If we get a second signal, we immediately exit, so something like
			// https://github.com/k6io/k6/issues/971 never happens again
			gs.osExit(int(exitcodes.ExternalAbort))
		case <-done:
			return
		}
	}()

	return func() {
		close(done)
		gs.signalStop(sigC)
	}
}

// Generate execution description for both cloud and local execution.
// TODO: Clean this up as part of #1499 or #1427
func getExecutionDescription(
	applyTheme func(string) string, execution, filename, outputOverride string,
	conf Config, et *lib.ExecutionTuple, execPlan []lib.ExecutionStep,
	outputs []output.Output,
) string {
	buf := &strings.Builder{}
	fmt.Fprintf(buf, "  execution: %s\n", applyTheme(execution))
	fmt.Fprintf(buf, "     script: %s\n", applyTheme(filename))

	var outputDescriptions []string
	switch {
	case outputOverride != "":
		outputDescriptions = []string{outputOverride}
	case len(outputs) == 0:
		outputDescriptions = []string{"-"}
	default:
		for _, out := range outputs {
			outputDescriptions = append(outputDescriptions, out.Description())
		}
	}

	fmt.Fprintf(buf, "     output: %s\n", applyTheme(strings.Join(outputDescriptions, ", ")))
	fmt.Fprintf(buf, "\n")

	maxDuration, _ := lib.GetEndOffset(execPlan)
	executorConfigs := conf.Scenarios.GetSortedConfigs()

	scenarioDesc := "1 scenario"
	if len(executorConfigs) > 1 {
		scenarioDesc = fmt.Sprintf("%d scenarios", len(executorConfigs))
	}

	fmt.Fprintf(buf, "  scenarios: %s\n", applyTheme(fmt.Sprintf(
		"(%.2f%%) %s, %d max VUs, %s max duration (incl. graceful stop):",
		conf.ExecutionSegment.FloatLength()*100, scenarioDesc,
		lib.GetMaxPossibleVUs(execPlan), maxDuration.Round(100*time.Millisecond)),
	))
	for _, ec := range executorConfigs {
		fmt.Fprintf(buf, "           * %s: %s\n",
			ec.GetName(), ec.GetDescription(et))
	}
	fmt.Fprintf(buf, "\n")

	return buf.String()
}
