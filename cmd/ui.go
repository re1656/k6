package cmd

import (
	"go.k6.io/k6/cmd/state"
	"go.k6.io/k6/ui/console/pb"
)

func maybePrintBanner(gs *state.GlobalState) {
	if !gs.Flags.Quiet {
		gs.Console.Printf("\n%s\n\n", gs.Console.Banner())
	}
}

func maybePrintBar(gs *state.GlobalState, bar *pb.ProgressBar) {
	if !gs.Flags.Quiet {
		gs.Console.PrintBar(bar)
	}
}
