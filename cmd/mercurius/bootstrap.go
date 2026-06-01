package main

import (
	"errors"
	"fmt"

	"github.com/michaelquigley/mercurius/internal/bootstrap"
	"github.com/spf13/cobra"
)

func newBootstrapCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:          "bootstrap",
		Short:        "write a starter mercurius.yaml into the current directory",
		Long:         "Write the embedded starter `mercurius.yaml` template into the current directory. Refuses to overwrite an existing file unless --force is passed.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := bootstrap.Write(".", force)
			if errors.Is(err, bootstrap.ErrExists) {
				fmt.Fprintf(cmd.ErrOrStderr(), "mercurius.yaml already exists at '%s'; pass --force to overwrite\n", path)
				return err
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote '%s'\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "next: edit the calibration and reviewer fields, then see docs/current/agent-guide.md for how to drive a review well.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing mercurius.yaml")
	return cmd
}
