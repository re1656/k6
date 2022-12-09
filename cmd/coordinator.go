package cmd

import (
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.k6.io/k6/errext"
	"go.k6.io/k6/errext/exitcodes"
	"go.k6.io/k6/execution"
	"go.k6.io/k6/execution/distributed"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/metrics/engine"
	"google.golang.org/grpc"
)

// cmdCoordinator handles the `k6 coordinator` sub-command
type cmdCoordinator struct {
	gs            *globalState
	gRPCAddress   string
	instanceCount int
}

//nolint:funlen // TODO: split apart
func (c *cmdCoordinator) run(cmd *cobra.Command, args []string) (err error) {
	ctx, runAbort := execution.NewTestRunContext(c.gs.ctx, c.gs.logger)

	tests, err := loadAndConfigureLocalTests(c.gs, cmd, args, getPartialConfig)
	if err != nil {
		return err
	}

	// TODO: refactor at some point, this limits us to handleSummary() from the first test
	firstTest := tests[0]
	// TODO: refactor - this is safe, preInitState is the same for all tests,
	// but it's still icky to get it that way
	preInitState := firstTest.preInitState
	metricsEngine, err := engine.NewMetricsEngine(preInitState.Registry, c.gs.logger)
	if err != nil {
		return err
	}

	testArchives := make([]*lib.Archive, len(tests))
	for i, test := range tests {
		runState, err := test.buildTestRunState(test.consolidatedConfig.Options)
		if err != nil {
			return err
		}

		// We get the thresholds from all tests
		testArchives[i] = runState.Runner.MakeArchive()
		err = metricsEngine.InitSubMetricsAndThresholds(
			test.derivedConfig.Options,
			preInitState.RuntimeOptions.NoThresholds.Bool,
		)
		if err != nil {
			return err
		}
	}

	coordinator, err := distributed.NewCoordinatorServer(
		c.instanceCount, testArchives, metricsEngine, c.gs.logger,
	)
	if err != nil {
		return err
	}

	if !preInitState.RuntimeOptions.NoSummary.Bool {
		defer func() {
			c.gs.logger.Debug("Generating the end-of-test summary...")
			summaryResult, serr := firstTest.initRunner.HandleSummary(ctx, &lib.Summary{
				Metrics:         metricsEngine.ObservedMetrics,
				RootGroup:       firstTest.initRunner.GetDefaultGroup(),
				TestRunDuration: coordinator.GetCurrentTestRunDuration(),
				NoColor:         c.gs.flags.noColor,
				UIState: lib.UIState{
					IsStdOutTTY: c.gs.stdOut.isTTY,
					IsStdErrTTY: c.gs.stdErr.isTTY,
				},
			})
			if serr == nil {
				serr = handleSummaryResult(c.gs.fs, c.gs.stdOut, c.gs.stdErr, summaryResult)
			}
			if serr != nil {
				c.gs.logger.WithError(serr).Error("Failed to handle the end-of-test summary")
			}
		}()
	}

	if !preInitState.RuntimeOptions.NoThresholds.Bool {
		getCurrentTestDuration := coordinator.GetCurrentTestRunDuration
		finalizeThresholds := metricsEngine.StartThresholdCalculations(nil, getCurrentTestDuration, runAbort)

		defer func() {
			// This gets called after all of the outputs have stopped, so we are
			// sure there won't be any more metrics being sent.
			c.gs.logger.Debug("Finalizing thresholds...")
			breachedThresholds := finalizeThresholds()
			if len(breachedThresholds) > 0 {
				tErr := errext.WithAbortReasonIfNone(
					errext.WithExitCodeIfNone(
						fmt.Errorf("thresholds on metrics '%s' have been breached", strings.Join(breachedThresholds, ", ")),
						exitcodes.ThresholdsHaveFailed,
					), errext.AbortedByThresholdsAfterTestEnd)

				if err == nil {
					err = tErr
				} else {
					c.gs.logger.WithError(tErr).Debug("Breached thresholds, but test already exited with another error")
				}
			}
		}()
	}

	c.gs.logger.Infof("Starting gRPC server on %s", c.gRPCAddress)
	listener, err := net.Listen("tcp", c.gRPCAddress)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer() // TODO: add auth and a whole bunch of other options
	distributed.RegisterDistributedTestServer(grpcServer, coordinator)

	go func() {
		err := grpcServer.Serve(listener)
		c.gs.logger.Debugf("gRPC server end: %s", err)
	}()
	coordinator.Wait()
	c.gs.logger.Infof("All done!")
	return nil
}

func (c *cmdCoordinator) flagSet() *pflag.FlagSet {
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	flags.SortFlags = false
	flags.AddFlagSet(optionFlagSet())
	flags.AddFlagSet(runtimeOptionFlagSet(false))
	flags.StringVar(&c.gRPCAddress, "grpc-addr", "localhost:6566", "address on which to bind the gRPC server")
	flags.IntVar(&c.instanceCount, "instance-count", 1, "number of distributed instances")
	return flags
}

func getCmdCoordnator(gs *globalState) *cobra.Command {
	c := &cmdCoordinator{
		gs: gs,
	}

	coordinatorCmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Start a distributed load test",
		Long:  `TODO`,
		RunE:  c.run,
	}

	coordinatorCmd.Flags().SortFlags = false
	coordinatorCmd.Flags().AddFlagSet(c.flagSet())

	return coordinatorCmd
}
