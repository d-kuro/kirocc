// Command kirocc-jev-replay re-issues a recorded Jev judgment against the live
// TypeSafe endpoint, for debugging and tuning the auto-mode classifier. It reads
// the replay file written by `kirocc -safeguard-record-file`, picks one record,
// and prints the original verdict alongside a fresh one. With -questions it
// swaps in a modified questions map, so a proposed classification prompt can be
// A/B'd against a captured case on identical inputs.
//
// It makes a live, billed Jev call. It is a developer tool, not part of the
// bridge.
//
// Usage:
//
//	kirocc-jev-replay -file ~/.cache/kirocc-jev.jsonl -line 12
//	kirocc-jev-replay -file ~/.cache/kirocc-jev.jsonl -line 12 -questions new-questions.json
package main

import (
	"context"
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d-kuro/kirocc/internal/safeguard"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("kirocc-jev-replay", flag.ContinueOnError)
	file := fs.String("file", defaultRecordFile(), "replay JSON-lines file written by kirocc -safeguard-record-file")
	line := fs.Int("line", 0, "1-based record number to replay (0 = last)")
	keyFile := fs.String("key-file", defaultKeyFile(), "TypeSafe key file (or set TYPESAFE_API_KEY)")
	questionsPath := fs.String("questions", "", "optional JSON file with a modified questions map to A/B against the recorded inputs")
	endpoint := fs.String("endpoint", "", "override the TypeSafe endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	if key == "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return fmt.Errorf("no TYPESAFE_API_KEY and cannot read key file %s: %w", *keyFile, err)
		}
		key = strings.TrimSpace(string(b))
	}

	var opts []safeguard.Option
	if *endpoint != "" {
		opts = append(opts, safeguard.WithEndpoint(*endpoint))
	}
	c := safeguard.New(key, opts...)
	if c == nil {
		return fmt.Errorf("empty TypeSafe key")
	}

	var override map[string]safeguard.Question
	if *questionsPath != "" {
		qb, err := os.ReadFile(*questionsPath)
		if err != nil {
			return fmt.Errorf("read questions: %w", err)
		}
		if err := json.Unmarshal(qb, &override); err != nil {
			return fmt.Errorf("parse questions: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.ReplayFile(ctx, *file, *line, override)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	return nil
}

func defaultRecordFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "kirocc-jev.jsonl")
}

func defaultKeyFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "kiro", "typesafe-key")
}
