// labcli is an explicit developer test harness, not an application approval path.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/destinywans/redstring/internal/connectors"
	"os"
	"time"
)

func main() {
	config := flag.String("config", ".local/aiops-canvas-demo/connector.json", "connector config path")
	revision := flag.String("revision", "1", "target revision")
	planPath := flag.String("plan", "", "immutable plan JSON file")
	operation := flag.String("operation", "", "explicit operation ID for approved lab execution")
	flag.Parse()
	if flag.NArg() != 1 {
		fatal(fmt.Errorf("command: evidence | propose | execute | reconcile | observe"))
	}
	c, e := connectors.LoadConfig(*config)
	if e != nil {
		fatal(e)
	}
	k, e := connectors.New(c)
	if e != nil {
		fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out any
	switch flag.Arg(0) {
	case "evidence":
		out, e = k.ReadEvidence(ctx)
	case "propose":
		out, e = k.ProposeRollback(ctx, *revision)
	case "execute", "reconcile":
		var p connectors.RollbackPlan
		b, err := os.ReadFile(*planPath)
		if err != nil {
			fatal(err)
		}
		if err = json.Unmarshal(b, &p); err != nil {
			fatal(err)
		}
		if *operation == "" {
			fatal(fmt.Errorf("explicit -operation required"))
		}
		if flag.Arg(0) == "execute" {
			out, e = k.Execute(ctx, p, *operation)
		} else {
			out, e = k.Reconcile(ctx, p, *operation)
		}
	case "observe":
		out, e = k.Observe(ctx, 3*time.Minute)
	default:
		fatal(fmt.Errorf("unknown command"))
	}
	if out != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	}
	if e != nil {
		fatal(e)
	}
}
func fatal(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
