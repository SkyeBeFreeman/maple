package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// emit is the single output path: JSON of v with --json, otherwise human.
func emit(cmd *cobra.Command, v any, human func(w *strings.Builder)) error {
	w := cmd.OutOrStdout()
	if asJSON(cmd) {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	if human == nil {
		return nil
	}
	// Built first so a write error surfaces once instead of per line.
	var b strings.Builder
	human(&b)
	_, err := io.WriteString(w, b.String())
	return err
}

func asJSON(cmd *cobra.Command) bool {
	on, _ := cmd.Flags().GetBool("json")
	return on
}
