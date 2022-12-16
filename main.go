package main

import (
	"context"

	"go.k6.io/k6/cmd"
	"go.k6.io/k6/cmd/state"
)

func main() {
	cmd.Execute(state.NewGlobalState(context.Background()))
}
