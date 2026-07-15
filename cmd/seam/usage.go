package main

// seam usage -- roll-up from the usage_summary MCP tool.

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
)

func runUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cli, _, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	out, err := callTool(ctx, cli, "usage_summary", nil)
	if err != nil {
		return err
	}
	mem, _ := out["memories"].(map[string]any)
	ret, _ := out["retrieval"].(map[string]any)
	fmt.Printf("memories: %d active\n", int(num(mem["active"])))
	printCounts("  by kind", mem["byKind"])
	fmt.Printf("notes:    %d\n", int(num(out["notes"])))
	printCounts("sessions", out["sessions"])
	printCounts("tasks", out["tasks"])
	fmt.Printf("retrieval: %d injections, %d reads (%d%% read-after-inject)\n",
		int(num(ret["injections"])), int(num(ret["reads"])), pctOf(num(ret["reads"]), num(ret["injections"])))
	if top, ok := ret["topInjected"].([]any); ok && len(top) > 0 {
		fmt.Println("  most injected:")
		for _, t := range top {
			m, _ := t.(map[string]any)
			fmt.Printf("    %-32s %dx\n", str(m["name"]), int(num(m["count"])))
		}
	}
	printCounts("proposals", out["gardenerPending"])
	return nil
}

// printCounts renders a map[string]number as "label: k=v k=v".
func printCounts(label string, v any) {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		fmt.Printf("%s: (none)\n", label)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, int(num(m[k]))))
	}
	fmt.Printf("%s: %s\n", label, strings.Join(parts, " "))
}
