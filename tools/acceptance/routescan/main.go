// Command routescan lists the server's API routes and their V3 permissions by
// reading the source with go/ast, without building or starting the server.
//
//	go run ./tools/acceptance/routescan [-root .] [-o routes.json]
//
// It evaluates every non-test function under internal/ and cmd/ just far
// enough to resolve the arguments of MarkV3Route / MarkV3ObjectRoute and of
// route registrations: string literals, constants and variables, route tables
// (slices of structs or strings, maps) walked by range loops, and if branches
// whose condition compares known strings. The JSON output has
//
//   - routes: every pattern marked for V3, with module, action, object_scoped,
//     where the pattern literal is (source), the mark call (mark_site) and how
//     the pattern reached it (registration: literal | table | variable);
//   - legacy_only: /api/v1 patterns registered but never marked, which the V3
//     middleware answers with 501 v3_not_implemented;
//   - exempt: patterns let through unmarked by v3AuthOrInstance (auth_or_instance)
//     or v3SignedPushRoute (signed_push);
//   - diagnostics: calls it could not resolve. unresolved_mark and
//     conflicting_marks are fatal: the exit status is 2 and the matrix must not
//     be trusted until the scanner or the source is fixed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	dirs := flag.String("dirs", "internal,cmd", "comma-separated directories under root to scan")
	out := flag.String("o", "", "write JSON here instead of stdout")
	flag.Parse()
	res, err := Scan(*root, strings.Split(*dirs, ","))
	if err != nil {
		fmt.Fprintln(os.Stderr, "routescan:", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "routescan:", err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(*out, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "routescan:", err)
		os.Exit(1)
	}
	s := res.Summary
	fmt.Fprintf(os.Stderr, "routescan: %d V3 routes (%d in the matrix, %d writes; %d self/composite), %d legacy-only, %d exempt, %d diagnostics (%d fatal)\n",
		s.V3Routes, s.MatrixRoutes, s.MatrixWrites, s.SelfOrComposite, s.LegacyOnly, s.Exempt, len(res.Diagnostics), s.Fatal)
	for _, d := range res.Diagnostics {
		if fatalKinds[d.Kind] {
			fmt.Fprintf(os.Stderr, "  %s %s: %s\n", d.Kind, d.Site, d.Message)
		}
	}
	if s.Fatal > 0 {
		os.Exit(2)
	}
}
