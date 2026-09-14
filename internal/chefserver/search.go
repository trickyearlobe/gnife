package chefserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/trickyearlobe/gnife/internal/deps"
)

// ---------------------------------------------------------------------------
// Query language: a practical subset of Solr/Lucene as used in Chef searches.
//
//   field:value   field:val*   field:"a phrase"   field:[1 TO 5]   field:{a TO b}
//   a AND b   a OR b   NOT a   -a   +a   (grouping)   bare terms match any field
//
// Whitespace between clauses means AND (Chef's Solr schema sets the default
// operator to AND). Fields are the flattened attribute paths Chef indexes:
// nested keys joined with "_", at every suffix (kernel_machine, machine).
// ---------------------------------------------------------------------------

type query interface {
	match(doc *document) bool
}

type document struct {
	fields map[string][]string // flattened key -> values
	all    []string            // every "key:value" and value, for bare terms
}

type termQuery struct {
	field string // "" = any field
	value string // may hold * and ?
}

type rangeQuery struct {
	field     string
	lo, hi    string // "*" = unbounded
	inclusive bool
}

type boolQuery struct {
	op    string // "AND" | "OR"
	left  query
	right query
}

type notQuery struct{ q query }

func (t termQuery) match(d *document) bool {
	if t.field == "" {
		for _, v := range d.all {
			if glob(t.value, v) {
				return true
			}
		}
		return false
	}
	for _, v := range d.fields[t.field] {
		if glob(t.value, v) {
			return true
		}
	}
	return false
}

func (r rangeQuery) match(d *document) bool {
	for _, v := range d.fields[r.field] {
		if inRange(v, r.lo, r.hi, r.inclusive) {
			return true
		}
	}
	return false
}

func (b boolQuery) match(d *document) bool {
	if b.op == "OR" {
		return b.left.match(d) || b.right.match(d)
	}
	return b.left.match(d) && b.right.match(d)
}

func (n notQuery) match(d *document) bool { return !n.q.match(d) }

// glob matches with * and ? wildcards.
func glob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == s
	}
	return globMatch(pattern, s)
}

func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for i := 0; i <= len(s); i++ {
				if globMatch(p[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

// inRange compares numerically when both sides parse as numbers, else as strings.
func inRange(v, lo, hi string, inclusive bool) bool {
	cmp := func(a, b string) int {
		fa, errA := strconv.ParseFloat(a, 64)
		fb, errB := strconv.ParseFloat(b, 64)
		if errA == nil && errB == nil {
			switch {
			case fa < fb:
				return -1
			case fa > fb:
				return 1
			}
			return 0
		}
		return strings.Compare(a, b)
	}
	if lo != "*" {
		c := cmp(v, lo)
		if c < 0 || c == 0 && !inclusive {
			return false
		}
	}
	if hi != "*" {
		c := cmp(v, hi)
		if c > 0 || c == 0 && !inclusive {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

type parser struct {
	toks []string
	pos  int
}

// tokenize splits on whitespace, keeping quoted phrases, parentheses,
// brackets and braces as tokens.
func tokenize(q string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '(' || c == ')':
			flush()
			toks = append(toks, string(c))
		case c == '[' || c == '{':
			// range: read through the matching close
			end := strings.IndexAny(q[i:], "]}")
			if end < 0 {
				return nil, fmt.Errorf("unterminated range")
			}
			cur.WriteString(q[i : i+end+1])
			i += end
		case c == '"':
			end := strings.IndexByte(q[i+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated phrase")
			}
			cur.WriteString(q[i : i+end+2])
			i += end + 1
		case c == '\\' && i+1 < len(q):
			cur.WriteByte(q[i+1])
			i++
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks, nil
}

// parseQuery parses a Chef search query.
func parseQuery(q string) (query, error) {
	q = strings.TrimSpace(q)
	if q == "" || q == "*:*" {
		return termQuery{value: "*"}, nil
	}
	toks, err := tokenize(q)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	out, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, fmt.Errorf("unexpected %q", p.toks[p.pos])
	}
	return out, nil
}

func (p *parser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *parser) parseOr() (query, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek() == "OR" || p.peek() == "||" {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = boolQuery{op: "OR", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (query, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == "" || t == ")" || t == "OR" || t == "||" {
			return left, nil
		}
		if t == "AND" || t == "&&" {
			p.pos++
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = boolQuery{op: "AND", left: left, right: right}
	}
}

func (p *parser) parseUnary() (query, error) {
	t := p.peek()
	switch {
	case t == "":
		return nil, fmt.Errorf("unexpected end of query")
	case t == "NOT" || t == "!":
		p.pos++
		q, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notQuery{q}, nil
	case strings.HasPrefix(t, "-") && len(t) > 1:
		p.toks[p.pos] = t[1:]
		q, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notQuery{q}, nil
	case strings.HasPrefix(t, "+") && len(t) > 1:
		p.toks[p.pos] = t[1:]
		return p.parseUnary()
	case t == "(":
		p.pos++
		q, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return q, nil
	}
	p.pos++
	return parseTerm(t)
}

func parseTerm(t string) (query, error) {
	field, value := "", t
	if i := strings.IndexByte(t, ':'); i > 0 && !strings.HasPrefix(t, "\"") {
		field, value = t[:i], t[i+1:]
	}
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") {
		inclusive := value[0] == '['
		inner := strings.TrimSpace(value[1 : len(value)-1])
		lo, hi, ok := strings.Cut(inner, " TO ")
		if !ok {
			return nil, fmt.Errorf("bad range %q", value)
		}
		if field == "" {
			return nil, fmt.Errorf("range needs a field: %q", t)
		}
		return rangeQuery{field: field, lo: strings.TrimSpace(lo), hi: strings.TrimSpace(hi), inclusive: inclusive}, nil
	}
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && len(value) >= 2 {
		value = value[1 : len(value)-1]
	}
	return termQuery{field: field, value: value}, nil
}

// ---------------------------------------------------------------------------
// Indexing
// ---------------------------------------------------------------------------

// index flattens an object the way Chef's indexer does: nested keys joined
// by "_" at every suffix, arrays exploded, plus role:/recipe: derived from
// the run list and, for nodes, attributes merged by precedence.
func index(kind string, obj map[string]any) *document {
	d := &document{fields: map[string][]string{}}
	var flat map[string]any
	switch kind {
	case "node":
		flat = map[string]any{}
		for _, top := range []string{"default", "normal", "override", "automatic"} {
			if m, ok := obj[top].(map[string]any); ok {
				merge(flat, m)
			}
		}
		for _, k := range []string{"name", "chef_environment", "run_list", "policy_name", "policy_group", "chef_type"} {
			if v, ok := obj[k]; ok && v != nil {
				flat[k] = v
			}
		}
	case "data_bag_item":
		flat = map[string]any{}
		if raw, ok := obj["raw_data"].(map[string]any); ok {
			merge(flat, raw)
		} else {
			merge(flat, obj)
		}
	default:
		flat = obj
	}
	walkIndex(d, nil, flat)
	if rl, ok := flat["run_list"].([]any); ok {
		for _, item := range rl {
			s, _ := item.(string)
			it := deps.ParseRunListItem(s)
			d.add(it.Kind, it.Name)
			if it.Kind == "recipe" && !strings.Contains(it.Name, "::") {
				d.add("recipe", it.Name+"::default")
			}
		}
	}
	return d
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
			cp := map[string]any{}
			merge(cp, sm)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

func (d *document) add(field, value string) {
	d.fields[field] = append(d.fields[field], value)
	d.all = append(d.all, value, field+":"+value)
}

func walkIndex(d *document, path []string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			walkIndex(d, append(path, k), child)
		}
	case []any:
		for _, e := range t {
			walkIndex(d, path, e)
		}
	default:
		val := scalar(v)
		// every suffix of the path: a_b_c, b_c, c
		for i := range path {
			d.add(strings.Join(path[i:], "_"), val)
		}
	}
}

func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------------------
// Endpoint
// ---------------------------------------------------------------------------

// routeSearch serves GET/POST /search[/INDEX].
func (s *Server) routeSearch(req *request, o *Org, rest []string, url func(string, string) string) (int, any, *apiErr) {
	if len(rest) == 0 {
		out := map[string]string{}
		for _, idx := range []string{"client", "environment", "node", "role"} {
			out[idx] = url("search", idx)
		}
		for bag := range o.DataBags {
			out[bag] = url("search", bag)
		}
		return 200, out, nil
	}
	if len(rest) != 1 {
		return 0, nil, fail(404, "no route")
	}
	idx := rest[0]
	qs := req.r.URL.Query()
	q, err := parseQuery(qs.Get("q"))
	if err != nil {
		return 0, nil, fail(400, "invalid search query: %v", err)
	}
	var objects map[string]json.RawMessage
	kind := idx
	switch idx {
	case "node":
		objects = o.Nodes
	case "role":
		objects = o.Roles
	case "environment":
		objects = o.Environments
	case "client":
		objects = o.Clients
	default:
		bag, ok := o.DataBags[idx]
		if !ok {
			return 0, nil, fail(404, "I don't know how to search for %s data objects.", idx)
		}
		objects = bag
		kind = "data_bag_item"
	}

	var partial map[string][]string
	if req.r.Method == http.MethodPost && len(req.body) > 0 {
		if err := json.Unmarshal(req.body, &partial); err != nil {
			return 0, nil, fail(400, "invalid partial search body")
		}
	}

	type hit struct {
		name string
		obj  map[string]any
		raw  json.RawMessage
	}
	var hits []hit
	for name, raw := range objects {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) != nil {
			continue
		}
		if kind == "data_bag_item" {
			obj = map[string]any{"name": "data_bag_item_" + idx + "_" + name, "json_class": "Chef::DataBagItem", "chef_type": "data_bag_item", "data_bag": idx, "raw_data": obj}
		}
		if kind == "client" {
			obj["public_key"] = o.ClientKeys[name]["default"]
		}
		if q.match(index(kind, obj)) {
			hits = append(hits, hit{name: name, obj: obj, raw: raw})
		}
	}
	sortField := qs.Get("sort")
	sort.Slice(hits, func(i, j int) bool {
		if sortField != "" && sortField != "X_CHEF_id_CHEF_X asc" {
			return hits[i].name < hits[j].name
		}
		return hits[i].name < hits[j].name
	})

	start, _ := strconv.Atoi(qs.Get("start"))
	rows, _ := strconv.Atoi(qs.Get("rows"))
	if rows <= 0 {
		rows = 1000
	}
	total := len(hits)
	if start > total {
		start = total
	}
	end := min(start+rows, total)
	page := []any{}
	for _, h := range hits[start:end] {
		if partial != nil {
			data := map[string]any{}
			merged := index(kind, h.obj) // reuse flattening rules for lookup precedence
			_ = merged
			for key, path := range partial {
				data[key] = lookup(kind, h.obj, path)
			}
			page = append(page, map[string]any{"url": url(idx+"s", h.name), "data": data})
			continue
		}
		if kind == "data_bag_item" {
			page = append(page, h.obj)
		} else {
			page = append(page, h.raw)
		}
	}
	return 200, map[string]any{"total": total, "start": start, "rows": page}, nil
}

// lookup resolves a partial-search path against an object. For nodes the
// path is looked up through merged attributes, then the top level.
func lookup(kind string, obj map[string]any, path []string) any {
	get := func(root any) (any, bool) {
		cur := root
		for _, p := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = m[p]
			if !ok {
				return nil, false
			}
		}
		return cur, true
	}
	if kind == "node" {
		merged := map[string]any{}
		for _, top := range []string{"default", "normal", "override", "automatic"} {
			if m, ok := obj[top].(map[string]any); ok {
				merge(merged, m)
			}
		}
		if v, ok := get(merged); ok {
			return v
		}
	}
	if kind == "data_bag_item" {
		if raw, ok := obj["raw_data"]; ok {
			if v, ok := get(raw); ok {
				return v
			}
		}
	}
	if v, ok := get(obj); ok {
		return v
	}
	return nil
}
