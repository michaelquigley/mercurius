package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/michaelquigley/mercurius/internal/broker"
	"github.com/michaelquigley/mercurius/internal/config"
	"github.com/michaelquigley/mercurius/internal/prompt"
	"github.com/spf13/cobra"
)

// previewSnapshotSentinel is the snapshot path used in preview output. preview
// has no real snapshot directory; the sentinel makes the absence visible in
// the prompt without inventing a path. The reviewer does not act on snapshot
// paths at runtime so the sentinel is harmless to round behavior.
const previewSnapshotSentinel = "(preview)"

// safePreviewArtifactName mirrors the broker's artifact name validator so the
// preview CLI rejects the same names that open_session would.
var safePreviewArtifactName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func newPreviewCommand(configPath *string) *cobra.Command {
	var reviewContext string
	var reviewFocus string
	var artifacts []string
	var maxFindings int
	var output string

	cmd := &cobra.Command{
		Use:          "preview",
		Short:        "render the round-1 review prompt for a config and a set of artifacts without running a reviewer",
		Long:         "preview reads a Mercurius config and the named artifacts, assembles the prompt that broker round 1 would send to the reviewer, and prints it. no session is created, no reviewer is dispatched, and no .mercurius/ writes happen. use this to iterate on review_focus or other config-shaped content before paying the cost of a real round.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			// preview is read-only: we deliberately do not call
			// EnsureLogDestination, so a config pointed at a missing
			// log_destination still produces a valid prompt.

			parsed, err := parsePreviewArtifacts(artifacts)
			if err != nil {
				return err
			}

			ctx, err := buildPreviewContext(cfg, parsed, reviewContext, reviewFocus, maxFindings)
			if err != nil {
				return err
			}

			text, _ := prompt.Build(ctx)
			return writePreviewOutput(cmd, text, output)
		},
	}
	cmd.Flags().StringVar(&reviewContext, "review-context", "", "override the configured review_context for this preview")
	cmd.Flags().StringVar(&reviewFocus, "review-focus", "", "override the configured review_focus for this preview")
	cmd.Flags().StringArrayVar(&artifacts, "artifact", nil, "artifact in name=path form; repeat for multiple artifacts")
	cmd.Flags().IntVar(&maxFindings, "max-findings", 0, "override the configured max_findings for this preview (0 keeps the config value)")
	cmd.Flags().StringVar(&output, "output", "", "write the assembled prompt to this file instead of stdout")
	return cmd
}

type previewArtifactSpec struct {
	name string
	path string
}

func parsePreviewArtifacts(specs []string) ([]previewArtifactSpec, error) {
	if len(specs) == 0 {
		return nil, errors.New("at least one --artifact name=path is required")
	}
	parsed := make([]previewArtifactSpec, 0, len(specs))
	seen := map[string]struct{}{}
	for _, spec := range specs {
		// split on the first '=' only so paths containing '=' are handled.
		name, path, ok := strings.Cut(spec, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--artifact '%s' must be in name=path form", spec)
		}
		if err := validatePreviewArtifactName(name); err != nil {
			return nil, err
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate artifact name '%s'", name)
		}
		seen[name] = struct{}{}
		parsed = append(parsed, previewArtifactSpec{name: name, path: path})
	}
	return parsed, nil
}

func validatePreviewArtifactName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("artifact name '%s' length is outside 1-64", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("artifact name '%s' is not allowed", name)
	}
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("artifact name '%s' cannot begin with '_' (reserved for broker meta files)", name)
	}
	if !safePreviewArtifactName.MatchString(name) {
		return fmt.Errorf("artifact name '%s' is unsafe", name)
	}
	return nil
}

func buildPreviewContext(cfg *config.Config, specs []previewArtifactSpec, reviewContextOverride string, reviewFocusOverride string, maxFindingsOverride int) (prompt.Request, error) {
	artifacts := make([]prompt.Artifact, 0, len(specs))
	for _, spec := range specs {
		absPath, err := filepath.Abs(spec.path)
		if err != nil {
			return prompt.Request{}, fmt.Errorf("artifact '%s': resolve path: %w", spec.name, err)
		}
		content, err := os.ReadFile(absPath)
		if err != nil {
			return prompt.Request{}, fmt.Errorf("artifact '%s': %w", spec.name, err)
		}
		hash := sha256.Sum256(content)
		artifacts = append(artifacts, prompt.Artifact{
			Name:         spec.name,
			SourcePath:   absPath,
			SnapshotPath: previewSnapshotSentinel,
			Hash:         "sha256:" + hex.EncodeToString(hash[:]),
			Content:      content,
			Inline:       false,
		})
	}

	maxFindings := cfg.MaxFindings
	if maxFindingsOverride > 0 {
		maxFindings = maxFindingsOverride
	}
	return prompt.Request{
		Artifacts:      artifacts,
		PriorDecisions: nil,
		ReviewContext:  selectPreviewOverride(reviewContextOverride, cfg.ReviewContext),
		ReviewFocus:    selectPreviewOverride(reviewFocusOverride, cfg.ReviewFocus),
		DecisionsLog:   broker.EmptySessionDecisionsLogText(),
		MaxFindings:    maxFindings,
	}, nil
}

// selectPreviewOverride applies the same trim-and-non-empty rule used by the
// session-level review_context / review_focus overrides: a whitespace-only CLI
// value is treated as absent and the config value is used.
func selectPreviewOverride(cliValue string, configValue string) string {
	if trimmed := strings.TrimSpace(cliValue); trimmed != "" {
		return trimmed
	}
	return configValue
}

func writePreviewOutput(cmd *cobra.Command, text string, outputPath string) error {
	if outputPath == "" {
		_, err := io.WriteString(cmd.OutOrStdout(), text)
		return err
	}
	return os.WriteFile(outputPath, []byte(text), 0o600)
}

