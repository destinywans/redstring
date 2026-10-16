package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Route is one pattern switched to the V3 matrix by MarkV3Route or
// MarkV3ObjectRoute. Module "" is an authenticated user's own data or a
// composite route that checks each result itself.
type Route struct {
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	Pattern      string   `json:"pattern"`
	Module       string   `json:"module"`
	Action       string   `json:"action"`
	ObjectScoped bool     `json:"object_scoped"`
	Write        bool     `json:"write"`
	Source       string   `json:"source"`       // file:line of the pattern string literal
	MarkSite     string   `json:"mark_site"`    // file:line of the MarkV3* call
	Registration string   `json:"registration"` // literal | table | variable
	RegisteredAt []string `json:"registered_at"`
	AlsoMarkedAt []string `json:"also_marked_at,omitempty"`
	Exempt       string   `json:"exempt,omitempty"`
}

// Registered is a pattern registered on the mux but not marked for V3.
type Registered struct {
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	Pattern      string   `json:"pattern"`
	Write        bool     `json:"write"`
	Source       string   `json:"source"`
	RegisteredAt []string `json:"registered_at"`
	Exempt       string   `json:"exempt,omitempty"` // auth_or_instance | signed_push
}

type Diagnostic struct {
	Kind    string `json:"kind"`
	Site    string `json:"site"`
	Message string `json:"message"`
}

type Summary struct {
	V3Routes        int            `json:"v3_routes"`
	MatrixRoutes    int            `json:"matrix_routes"` // module != ""
	MatrixWrites    int            `json:"matrix_writes"`
	SelfOrComposite int            `json:"self_or_composite"`
	LegacyOnly      int            `json:"legacy_only"`
	Exempt          int            `json:"exempt"`
	ByModule        map[string]int `json:"by_module"`
	ByRegistration  map[string]int `json:"by_registration"`
	Fatal           int            `json:"fatal_diagnostics"`
}

type Result struct {
	Tool        string       `json:"tool"`
	Roots       []string     `json:"roots"`
	Summary     Summary      `json:"summary"`
	Routes      []Route      `json:"routes"`
	LegacyOnly  []Registered `json:"legacy_only"`
	Exempt      []Registered `json:"exempt"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Fatal diagnostics mean the matrix may be incomplete or wrong.
var fatalKinds = map[string]bool{"unresolved_mark": true, "conflicting_marks": true}

var routePattern = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS) (/\S*)$`)

// Calls whose first argument is a route pattern and that put it on the mux.
// "register" is the function-typed parameter or local closure some modules use.
var registrars = map[string]bool{"Register": true, "RegisterPublic": true, "HandleFunc": true, "Handle": true, "register": true}

// ---- values ----------------------------------------------------------------

// A value is *strVal, *listVal, *recordVal, *mapVal, boolVal or nil (unknown).
type value interface{}

type strVal struct {
	s      string
	pos    token.Pos
	origin string // literal | table | computed
}

type listVal struct{ items []value }

type recordVal struct{ fields map[string]value }

type mapVal struct {
	keys  []string
	items map[string]value
}

type boolVal bool

// fromTable copies v, marking each string as read from a route table row.
func fromTable(v value) value {
	switch t := v.(type) {
	case *strVal:
		c := *t
		if c.origin == "literal" {
			c.origin = "table"
		}
		return &c
	case *listVal:
		out := &listVal{}
		for _, it := range t.items {
			out.items = append(out.items, fromTable(it))
		}
		return out
	case *recordVal:
		out := &recordVal{fields: map[string]value{}}
		for k, it := range t.fields {
			out.fields[k] = fromTable(it)
		}
		return out
	case *mapVal:
		out := &mapVal{keys: t.keys, items: map[string]value{}}
		for k, it := range t.items {
			out.items[k] = fromTable(it)
		}
		return out
	}
	return v
}

// ---- scopes ----------------------------------------------------------------

type scope struct {
	parent *scope
	vars   map[string]value
	pkg    *pkgInfo
	// A boundary scope runs code whose execution is uncertain (a branch of an
	// unknown condition, a loop over unknown items). Outer variables assigned
	// inside it become unknown afterwards.
	boundary bool
	escaped  map[string]bool
}

func newScope(parent *scope) *scope {
	s := &scope{parent: parent, vars: map[string]value{}}
	if parent != nil {
		s.pkg = parent.pkg
	}
	return s
}

func (s *scope) lookup(name string) value {
	for cur := s; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v
		}
	}
	if s.pkg != nil {
		return s.pkg.lookup(name)
	}
	return nil
}

func (s *scope) define(name string, v value) {
	if name != "_" {
		s.vars[name] = v
	}
}

func (s *scope) assign(name string, v value) {
	if name == "_" {
		return
	}
	for cur := s; cur != nil; cur = cur.parent {
		if _, ok := cur.vars[name]; ok {
			cur.vars[name] = v
			return
		}
		if cur.boundary {
			cur.vars[name] = v
			cur.escaped[name] = true
			return
		}
	}
	s.vars[name] = v
}

// uncertain runs fn in a boundary scope and forgets outer assignments.
func (s *scope) uncertain(fn func(*scope)) {
	b := newScope(s)
	b.boundary, b.escaped = true, map[string]bool{}
	fn(b)
	for name := range b.escaped {
		s.assign(name, nil)
	}
}

// ---- packages --------------------------------------------------------------

type pkgInfo struct {
	in      *interp
	decls   map[string]ast.Expr // package-level const and var values
	types   map[string]ast.Expr // package-level type definitions
	cache   map[string]value
	pending map[string]bool
	root    *scope
}

func (p *pkgInfo) lookup(name string) value {
	if v, ok := p.cache[name]; ok {
		return v
	}
	expr, ok := p.decls[name]
	if !ok || p.pending[name] {
		return nil
	}
	p.pending[name] = true
	v := p.in.eval(expr, p.root)
	delete(p.pending, name)
	p.cache[name] = v
	return v
}

// ---- interpreter -----------------------------------------------------------

type markRecord struct {
	pattern      *strVal
	module       string
	action       string
	objectScoped bool
	site         token.Pos
	via          string
}

type interp struct {
	fset      *token.FileSet
	root      string
	pkg       *pkgInfo
	marks     map[string][]markRecord
	regs      map[string][]token.Pos
	regSource map[string]token.Pos
	exempt    map[string]string
	diags     []Diagnostic
	seenDiag  map[string]bool
}

func (in *interp) site(pos token.Pos) string {
	p := in.fset.Position(pos)
	name := p.Filename
	if rel, err := filepath.Rel(in.root, name); err == nil {
		name = rel
	}
	return fmt.Sprintf("%s:%d", filepath.ToSlash(name), p.Line)
}

func (in *interp) diag(kind string, pos token.Pos, format string, args ...any) {
	d := Diagnostic{Kind: kind, Site: in.site(pos), Message: fmt.Sprintf(format, args...)}
	key := d.Kind + "|" + d.Site + "|" + d.Message
	if !in.seenDiag[key] {
		in.seenDiag[key] = true
		in.diags = append(in.diags, d)
	}
}

func (in *interp) execBlock(stmts []ast.Stmt, s *scope) {
	for _, st := range stmts {
		in.exec(st, s)
	}
}

func (in *interp) exec(st ast.Stmt, s *scope) {
	switch t := st.(type) {
	case *ast.DeclStmt:
		in.declare(t.Decl, s)
	case *ast.AssignStmt:
		in.assignStmt(t, s)
	case *ast.ExprStmt:
		in.eval(t.X, s)
	case *ast.BlockStmt:
		in.execBlock(t.List, newScope(s))
	case *ast.IfStmt:
		in.ifStmt(t, s)
	case *ast.RangeStmt:
		in.rangeStmt(t, s)
	case *ast.ForStmt:
		inner := newScope(s)
		if t.Init != nil {
			in.exec(t.Init, inner)
		}
		inner.uncertain(func(b *scope) {
			if t.Cond != nil {
				in.eval(t.Cond, b)
			}
			in.execBlock(t.Body.List, b)
			if t.Post != nil {
				in.exec(t.Post, b)
			}
		})
	case *ast.SwitchStmt:
		inner := newScope(s)
		if t.Init != nil {
			in.exec(t.Init, inner)
		}
		if t.Tag != nil {
			in.eval(t.Tag, inner)
		}
		in.clauses(t.Body, inner)
	case *ast.TypeSwitchStmt:
		inner := newScope(s)
		if t.Init != nil {
			in.exec(t.Init, inner)
		}
		in.exec(t.Assign, inner)
		in.clauses(t.Body, inner)
	case *ast.SelectStmt:
		in.clauses(t.Body, s)
	case *ast.ReturnStmt:
		for _, r := range t.Results {
			in.eval(r, s)
		}
	case *ast.DeferStmt:
		in.eval(t.Call, s)
	case *ast.GoStmt:
		in.eval(t.Call, s)
	case *ast.LabeledStmt:
		in.exec(t.Stmt, s)
	case *ast.SendStmt:
		in.eval(t.Chan, s)
		in.eval(t.Value, s)
	case *ast.IncDecStmt:
		in.eval(t.X, s)
	}
}

func (in *interp) clauses(body *ast.BlockStmt, s *scope) {
	for _, c := range body.List {
		s.uncertain(func(b *scope) {
			switch cc := c.(type) {
			case *ast.CaseClause:
				for _, e := range cc.List {
					in.eval(e, b)
				}
				in.execBlock(cc.Body, b)
			case *ast.CommClause:
				if cc.Comm != nil {
					in.exec(cc.Comm, b)
				}
				in.execBlock(cc.Body, b)
			}
		})
	}
}

func (in *interp) declare(d ast.Decl, s *scope) {
	gd, ok := d.(*ast.GenDecl)
	if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
		return
	}
	for _, spec := range gd.Specs {
		vs := spec.(*ast.ValueSpec)
		for i, name := range vs.Names {
			var v value
			if i < len(vs.Values) && len(vs.Values) == len(vs.Names) {
				v = in.eval(vs.Values[i], s)
			} else if len(vs.Values) > 0 {
				for _, e := range vs.Values {
					in.eval(e, s)
				}
			}
			s.define(name.Name, v)
		}
	}
}

func (in *interp) assignStmt(t *ast.AssignStmt, s *scope) {
	values := make([]value, len(t.Lhs))
	if len(t.Rhs) == len(t.Lhs) {
		for i, r := range t.Rhs {
			values[i] = in.eval(r, s)
		}
	} else {
		for _, r := range t.Rhs {
			in.eval(r, s)
		}
	}
	for i, l := range t.Lhs {
		id, ok := l.(*ast.Ident)
		if !ok {
			in.eval(l, s)
			continue
		}
		v := values[i]
		if t.Tok != token.DEFINE && t.Tok != token.ASSIGN {
			v = nil // +=, |= ...
			if t.Tok == token.ADD_ASSIGN {
				v = concat(s.lookup(id.Name), values[i])
			}
		}
		if t.Tok == token.DEFINE {
			s.define(id.Name, v)
		} else {
			s.assign(id.Name, v)
		}
	}
}

func (in *interp) ifStmt(t *ast.IfStmt, s *scope) {
	inner := newScope(s)
	if t.Init != nil {
		in.exec(t.Init, inner)
	}
	cond := in.eval(t.Cond, inner)
	if b, ok := cond.(boolVal); ok {
		if b {
			in.execBlock(t.Body.List, newScope(inner))
		} else if t.Else != nil {
			in.exec(t.Else, inner)
		}
		return
	}
	inner.uncertain(func(b *scope) { in.execBlock(t.Body.List, b) })
	if t.Else != nil {
		inner.uncertain(func(b *scope) { in.exec(t.Else, b) })
	}
}

func (in *interp) rangeStmt(t *ast.RangeStmt, s *scope) {
	x := in.eval(t.X, s)
	bind := func(iter *scope, key, val value) {
		for _, pair := range []struct {
			e ast.Expr
			v value
		}{{t.Key, key}, {t.Value, val}} {
			id, ok := pair.e.(*ast.Ident)
			if !ok {
				continue
			}
			if t.Tok == token.DEFINE {
				iter.define(id.Name, pair.v)
			} else {
				iter.assign(id.Name, pair.v)
			}
		}
	}
	switch c := x.(type) {
	case *listVal:
		for _, item := range c.items {
			iter := newScope(s)
			bind(iter, nil, fromTable(item))
			in.execBlock(t.Body.List, iter)
		}
	case *mapVal:
		for _, k := range c.keys {
			iter := newScope(s)
			bind(iter, &strVal{s: k, origin: "table"}, fromTable(c.items[k]))
			in.execBlock(t.Body.List, iter)
		}
	default:
		s.uncertain(func(b *scope) {
			bind(b, nil, nil)
			in.execBlock(t.Body.List, b)
		})
	}
}

func concat(a, b value) value {
	as, ok1 := a.(*strVal)
	bs, ok2 := b.(*strVal)
	if !ok1 || !ok2 {
		return nil
	}
	origin := "computed"
	if as.origin == "table" || bs.origin == "table" {
		origin = "table"
	}
	return &strVal{s: as.s + bs.s, pos: as.pos, origin: origin}
}

func (in *interp) eval(e ast.Expr, s *scope) value {
	switch t := e.(type) {
	case nil:
		return nil
	case *ast.BasicLit:
		if t.Kind == token.STRING {
			if v, err := strconv.Unquote(t.Value); err == nil {
				return &strVal{s: v, pos: t.Pos(), origin: "literal"}
			}
		}
		return nil
	case *ast.Ident:
		switch t.Name {
		case "true":
			if s.lookup("true") == nil {
				return boolVal(true)
			}
		case "false":
			if s.lookup("false") == nil {
				return boolVal(false)
			}
		}
		return s.lookup(t.Name)
	case *ast.ParenExpr:
		return in.eval(t.X, s)
	case *ast.BinaryExpr:
		return in.binary(t, s)
	case *ast.UnaryExpr:
		x := in.eval(t.X, s)
		if t.Op == token.NOT {
			if b, ok := x.(boolVal); ok {
				return !b
			}
			return nil
		}
		if t.Op == token.AND {
			return x
		}
		return nil
	case *ast.StarExpr:
		in.eval(t.X, s)
		return nil
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && pkg.Name == "http" && s.lookup("http") == nil && strings.HasPrefix(t.Sel.Name, "Method") {
			return &strVal{s: strings.ToUpper(strings.TrimPrefix(t.Sel.Name, "Method")), pos: t.Pos(), origin: "literal"}
		}
		if r, ok := in.eval(t.X, s).(*recordVal); ok {
			return r.fields[t.Sel.Name]
		}
		return nil
	case *ast.IndexExpr:
		x := in.eval(t.X, s)
		idx := in.eval(t.Index, s)
		switch c := x.(type) {
		case *mapVal:
			if k, ok := idx.(*strVal); ok {
				return c.items[k.s]
			}
		case *listVal:
			if lit, ok := t.Index.(*ast.BasicLit); ok && lit.Kind == token.INT {
				if n, err := strconv.Atoi(lit.Value); err == nil && n >= 0 && n < len(c.items) {
					return c.items[n]
				}
			}
		}
		return nil
	case *ast.SliceExpr:
		in.eval(t.X, s)
		return nil
	case *ast.TypeAssertExpr:
		in.eval(t.X, s)
		return nil
	case *ast.CompositeLit:
		return in.composite(t, t.Type, s)
	case *ast.FuncLit:
		fs := newScope(s)
		in.bindParams(t.Type, fs)
		fs.uncertain(func(b *scope) { in.execBlock(t.Body.List, b) })
		return nil
	case *ast.CallExpr:
		return in.call(t, s)
	case *ast.KeyValueExpr:
		in.eval(t.Key, s)
		return in.eval(t.Value, s)
	}
	return nil
}

func (in *interp) binary(t *ast.BinaryExpr, s *scope) value {
	switch t.Op {
	case token.LAND, token.LOR:
		x := in.eval(t.X, s)
		if xb, ok := x.(boolVal); ok {
			if (t.Op == token.LAND && !bool(xb)) || (t.Op == token.LOR && bool(xb)) {
				return xb
			}
		}
		y := in.eval(t.Y, s)
		xb, okx := x.(boolVal)
		yb, oky := y.(boolVal)
		switch {
		case okx && oky:
			if t.Op == token.LAND {
				return xb && yb
			}
			return xb || yb
		case t.Op == token.LAND && oky && !bool(yb):
			return boolVal(false)
		case t.Op == token.LOR && oky && bool(yb):
			return boolVal(true)
		}
		return nil
	}
	x, y := in.eval(t.X, s), in.eval(t.Y, s)
	switch t.Op {
	case token.ADD:
		return concat(x, y)
	case token.EQL, token.NEQ:
		xs, ok1 := x.(*strVal)
		ys, ok2 := y.(*strVal)
		if ok1 && ok2 {
			return boolVal((xs.s == ys.s) == (t.Op == token.EQL))
		}
		xb, ok1 := x.(boolVal)
		yb, ok2 := y.(boolVal)
		if ok1 && ok2 {
			return boolVal((xb == yb) == (t.Op == token.EQL))
		}
	}
	return nil
}

func (in *interp) bindParams(ft *ast.FuncType, s *scope) {
	for _, list := range []*ast.FieldList{ft.Params, ft.Results} {
		if list == nil {
			continue
		}
		for _, f := range list.List {
			for _, n := range f.Names {
				s.define(n.Name, nil)
			}
		}
	}
}

// resolveType follows package-level named types to their definition.
func (in *interp) resolveType(typ ast.Expr, s *scope) ast.Expr {
	for i := 0; i < 8 && typ != nil; i++ {
		switch t := typ.(type) {
		case *ast.Ident:
			if s.pkg == nil {
				return typ
			}
			def, ok := s.pkg.types[t.Name]
			if !ok {
				return typ
			}
			typ = def
		case *ast.ParenExpr:
			typ = t.X
		case *ast.StarExpr:
			typ = t.X
		default:
			return typ
		}
	}
	return typ
}

func structFields(st *ast.StructType) []string {
	var names []string
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			names = append(names, embeddedName(f.Type))
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

func embeddedName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func (in *interp) element(e ast.Expr, typ ast.Expr, s *scope) value {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	if cl, ok := e.(*ast.CompositeLit); ok && cl.Type == nil {
		return in.composite(cl, typ, s)
	}
	return in.eval(e, s)
}

func (in *interp) composite(cl *ast.CompositeLit, typ ast.Expr, s *scope) value {
	if cl.Type != nil {
		typ = cl.Type
	}
	switch t := in.resolveType(typ, s).(type) {
	case *ast.ArrayType:
		out := &listVal{}
		for _, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			out.items = append(out.items, in.element(el, t.Elt, s))
		}
		return out
	case *ast.MapType:
		out := &mapVal{items: map[string]value{}}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key := in.element(kv.Key, t.Key, s)
			val := in.element(kv.Value, t.Value, s)
			if k, ok := key.(*strVal); ok {
				if _, dup := out.items[k.s]; !dup {
					out.keys = append(out.keys, k.s)
				}
				out.items[k.s] = val
			}
		}
		sort.Strings(out.keys)
		return out
	case *ast.StructType:
		names := structFields(t)
		types := map[string]ast.Expr{}
		for _, f := range t.Fields.List {
			for _, n := range f.Names {
				types[n.Name] = f.Type
			}
		}
		out := &recordVal{fields: map[string]value{}}
		for i, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					out.fields[id.Name] = in.element(kv.Value, types[id.Name], s)
				}
				continue
			}
			if i < len(names) {
				out.fields[names[i]] = in.element(el, types[names[i]], s)
			}
		}
		return out
	}
	for _, el := range cl.Elts {
		in.eval(el, s)
	}
	return nil
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr: // generic instantiation
		return calleeName(f.X)
	}
	return ""
}

func (in *interp) call(c *ast.CallExpr, s *scope) value {
	in.eval(c.Fun, s)
	args := make([]value, len(c.Args))
	for i, a := range c.Args {
		args[i] = in.eval(a, s)
	}
	name := calleeName(c.Fun)
	switch name {
	case "MarkV3Route", "MarkV3ObjectRoute":
		if len(args) != 3 {
			in.diag("unresolved_mark", c.Pos(), "%s with %d arguments", name, len(args))
			return nil
		}
		var parts [3]*strVal
		for i, a := range args {
			sv, ok := a.(*strVal)
			if !ok {
				in.diag("unresolved_mark", c.Args[i].Pos(), "%s argument %d (%s) is not statically known", name, i+1, exprString(in.fset, c.Args[i]))
				return nil
			}
			parts[i] = sv
		}
		if !routePattern.MatchString(parts[0].s) {
			in.diag("unresolved_mark", c.Pos(), "%s pattern %q is not METHOD /path", name, parts[0].s)
			return nil
		}
		via := "variable"
		if _, lit := c.Args[0].(*ast.BasicLit); lit {
			via = "literal"
		} else if parts[0].origin == "table" {
			via = "table"
		}
		in.marks[parts[0].s] = append(in.marks[parts[0].s], markRecord{
			pattern: parts[0], module: parts[1].s, action: parts[2].s,
			objectScoped: name == "MarkV3ObjectRoute", site: c.Pos(), via: via,
		})
	case "MarkV3FailureAudit":
	default:
		if len(args) == 0 {
			return nil
		}
		sv, ok := args[0].(*strVal)
		if !ok || !routePattern.MatchString(sv.s) {
			return nil
		}
		if !registrars[name] {
			in.diag("ignored_pattern_call", c.Pos(), "%s(%q, ...) is not a known registration call", name, sv.s)
			return nil
		}
		in.regs[sv.s] = append(in.regs[sv.s], c.Pos())
		if _, ok := in.regSource[sv.s]; !ok {
			in.regSource[sv.s] = sv.pos
		}
	}
	return nil
}

func exprString(fset *token.FileSet, e ast.Expr) string {
	start, end := fset.Position(e.Pos()), fset.Position(e.End())
	if start.Line == end.Line {
		return fmt.Sprintf("%T at col %d", e, start.Column)
	}
	return fmt.Sprintf("%T", e)
}

// exemptions reads v3AuthOrInstance and v3SignedPushRoute, the two places the
// V3 middleware lets an unmarked pattern through.
func (in *interp) exemptions(fd *ast.FuncDecl) {
	kind := map[string]string{"v3AuthOrInstance": "auth_or_instance", "v3SignedPushRoute": "signed_push"}[fd.Name.Name]
	if kind == "" || fd.Body == nil {
		return
	}
	method := ""
	var cases []*ast.BasicLit
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.BinaryExpr:
			if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Method" {
				if m, ok := in.eval(t.Y, newScope(nil)).(*strVal); ok {
					method = m.s
				}
			}
		case *ast.CaseClause:
			for _, e := range t.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					cases = append(cases, lit)
				}
			}
		}
		return true
	})
	for _, lit := range cases {
		v, _ := strconv.Unquote(lit.Value)
		if kind == "signed_push" {
			if method == "" {
				in.diag("exemption", lit.Pos(), "signed push path %s has no method check", v)
				continue
			}
			v = method + " " + v
		}
		in.exempt[v] = kind
	}
}

// ---- driver ----------------------------------------------------------------

// Scan parses the non-test Go files under root/dirs and evaluates every
// function body once.
func Scan(root string, dirs []string) (*Result, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	in := &interp{
		fset: token.NewFileSet(), root: absRoot,
		marks: map[string][]markRecord{}, regs: map[string][]token.Pos{}, regSource: map[string]token.Pos{},
		exempt: map[string]string{}, seenDiag: map[string]bool{},
	}
	packages := map[string][]*ast.File{}
	var order []string
	for _, d := range dirs {
		base := filepath.Join(absRoot, d)
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if name := e.Name(); path != base && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(in.fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			dir := filepath.Dir(path)
			if _, ok := packages[dir]; !ok {
				order = append(order, dir)
			}
			packages[dir] = append(packages[dir], f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(order)
	for _, dir := range order {
		files := packages[dir]
		pkg := &pkgInfo{in: in, decls: map[string]ast.Expr{}, types: map[string]ast.Expr{}, cache: map[string]value{}, pending: map[string]bool{}}
		pkg.root = &scope{vars: map[string]value{}, pkg: pkg}
		for _, f := range files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						pkg.types[sp.Name.Name] = sp.Type
					case *ast.ValueSpec:
						if len(sp.Values) == len(sp.Names) {
							for i, n := range sp.Names {
								pkg.decls[n.Name] = sp.Values[i]
							}
						}
					}
				}
			}
		}
		for _, f := range files {
			for _, d := range f.Decls {
				switch t := d.(type) {
				case *ast.FuncDecl:
					in.exemptions(t)
					if t.Body == nil {
						continue
					}
					fs := newScope(pkg.root)
					if t.Recv != nil {
						for _, r := range t.Recv.List {
							for _, n := range r.Names {
								fs.define(n.Name, nil)
							}
						}
					}
					in.bindParams(t.Type, fs)
					in.execBlock(t.Body.List, fs)
				case *ast.GenDecl:
					// Package-level initializers can register too (rare).
					for _, spec := range t.Specs {
						if vs, ok := spec.(*ast.ValueSpec); ok {
							for _, v := range vs.Values {
								if _, isLit := v.(*ast.CompositeLit); !isLit {
									in.eval(v, pkg.root)
								}
							}
						}
					}
				}
			}
		}
	}
	return in.result(dirs), nil
}

func split(pattern string) (string, string) {
	m := routePattern.FindStringSubmatch(pattern)
	return m[1], m[2]
}

func isWrite(method string) bool { return method != "GET" && method != "HEAD" }

func (in *interp) sites(ps []token.Pos) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range ps {
		s := in.site(p)
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func (in *interp) result(dirs []string) *Result {
	res := &Result{Tool: "tools/acceptance/routescan", Roots: dirs, Routes: []Route{}, LegacyOnly: []Registered{}, Exempt: []Registered{}}
	res.Summary.ByModule = map[string]int{}
	res.Summary.ByRegistration = map[string]int{}
	patterns := make([]string, 0, len(in.marks))
	for p := range in.marks {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		recs := in.marks[p]
		sort.SliceStable(recs, func(i, j int) bool { return in.site(recs[i].site) < in.site(recs[j].site) })
		first := recs[0]
		var also []string
		seenSite := map[string]bool{in.site(first.site): true}
		for _, r := range recs[1:] {
			if r.module != first.module || r.action != first.action || r.objectScoped != first.objectScoped {
				in.diag("conflicting_marks", r.site, "%s marked %s.%s (object=%v) here and %s.%s (object=%v) at %s",
					p, r.module, r.action, r.objectScoped, first.module, first.action, first.objectScoped, in.site(first.site))
			}
			if s := in.site(r.site); !seenSite[s] {
				seenSite[s] = true
				also = append(also, s)
			}
		}
		method, path := split(p)
		route := Route{
			Method: method, Path: path, Pattern: p, Module: first.module, Action: first.action,
			ObjectScoped: first.objectScoped, Write: isWrite(method), Source: in.site(first.pattern.pos),
			MarkSite: in.site(first.site), Registration: first.via, RegisteredAt: in.sites(in.regs[p]),
			AlsoMarkedAt: also, Exempt: in.exempt[p],
		}
		if len(in.regs[p]) == 0 {
			in.diag("marked_not_registered", first.site, "%s is marked for V3 but no registration call was found", p)
		}
		res.Routes = append(res.Routes, route)
		res.Summary.ByRegistration[route.Registration]++
		if route.Module == "" {
			res.Summary.SelfOrComposite++
		} else {
			res.Summary.MatrixRoutes++
			res.Summary.ByModule[route.Module]++
			if route.Write {
				res.Summary.MatrixWrites++
			}
		}
	}
	var regPatterns []string
	for p := range in.regs {
		regPatterns = append(regPatterns, p)
	}
	for p := range in.exempt {
		if _, ok := in.regs[p]; !ok {
			regPatterns = append(regPatterns, p)
		}
	}
	sort.Strings(regPatterns)
	for _, p := range regPatterns {
		if _, marked := in.marks[p]; marked {
			continue
		}
		method, path := split(p)
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		r := Registered{Method: method, Path: path, Pattern: p, Write: isWrite(method), RegisteredAt: in.sites(in.regs[p]), Exempt: in.exempt[p]}
		if pos, ok := in.regSource[p]; ok {
			r.Source = in.site(pos)
		}
		if r.Exempt != "" {
			if len(in.regs[p]) == 0 {
				in.diag("exempt_not_registered", token.NoPos, "%s is exempt (%s) but no registration call was found", p, r.Exempt)
			}
			res.Exempt = append(res.Exempt, r)
		} else {
			res.LegacyOnly = append(res.LegacyOnly, r)
		}
	}
	res.Summary.V3Routes = len(res.Routes)
	res.Summary.LegacyOnly = len(res.LegacyOnly)
	res.Summary.Exempt = len(res.Exempt)
	sort.SliceStable(in.diags, func(i, j int) bool {
		if in.diags[i].Kind != in.diags[j].Kind {
			return in.diags[i].Kind < in.diags[j].Kind
		}
		return in.diags[i].Site < in.diags[j].Site
	})
	res.Diagnostics = append([]Diagnostic{}, in.diags...)
	for _, d := range res.Diagnostics {
		if fatalKinds[d.Kind] {
			res.Summary.Fatal++
		}
	}
	return res
}
