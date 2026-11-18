package main

import (
	"errors"

	"github.com/michaelquigley/mercurius/internal/runner"
	"github.com/spf13/cobra"
)

// newRunRoundCommand is the hidden runner a server starts for each round. it
// inherits the runner lock on descriptor 3 and logs to stderr, which the
// launcher points at the round's _runner.log.
func newRunRoundCommand() *cobra.Command {
	var roundDir string

	cmd := &cobra.Command{
		Use:          "run-round",
		Short:        "run one prepared review round (started by the server)",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if roundDir == "" {
				return errors.New("--round-dir is required")
			}
			return runner.RunInherited(roundDir, runner.InheritedLockFD, runner.BuildReviewer)
		},
	}
	cmd.Flags().StringVar(&roundDir, "round-dir", "", "the prepared round directory")
	return cmd
}
