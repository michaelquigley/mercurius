package main

import (
	"errors"
	"fmt"

	"github.com/michaelquigley/mercurius/internal/bootstrap"
	"github.com/spf13/cobra"
)

func newBootstrapCommand(userConfigPath string) *cobra.Command {
	var force bool
	var user bool

	cmd := &cobra.Command{
		Use:          "bootstrap",
		Short:        "write a starter mercurius.yaml into the current directory, or the user-level config with --user",
		Long:         "Write the embedded starter `mercurius.yaml` template into the current directory. With --user, write the starter user-level config, which holds the reviewer for this installation, instead. Refuses to overwrite an existing file unless --force is passed.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if user {
				path, err := bootstrap.WriteUser(userConfigPath, force)
				if errors.Is(err, bootstrap.ErrExists) {
					fmt.Fprintf(cmd.ErrOrStderr(), "the user-level config already exists at '%s'; pass --force to overwrite\n", path)
					return err
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote '%s'\n", path)
				fmt.Fprintln(cmd.OutOrStdout(), "next: choose the reviewer block for this installation; every project uses it.")
				return nil
			}
			path, err := bootstrap.Write(".", force)
			if errors.Is(err, bootstrap.ErrExists) {
				fmt.Fprintf(cmd.ErrOrStderr(), "mercurius.yaml already exists at '%s'; pass --force to overwrite\n", path)
				return err
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote '%s'\n", path)
			fmt.Fprintf(cmd.OutOrStdout(), "next: edit the calibration fields; the reviewer comes from the user-level config '%s' (mercurius bootstrap --user writes one). see docs/current/agent-guide.md for how to drive a review well.\n", userConfigPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().BoolVar(&user, "user", false, "write the user-level config, which holds the reviewer, instead of the project's mercurius.yaml")
	return cmd
}
