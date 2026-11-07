package core

import (
	"database/sql"
	"sort"
	"strings"
	"unicode"
)

// rdfLinkReasonPrefix marks links that were derived from shared RDF entities.
// It lets the RDF link builder refresh its own links without touching the
// semantic links produced by the pair process.
const rdfLinkReasonPrefix = "RDF shared: "

// rdfTripleFields splits one simple Turtle triple line into subject, predicate
// and object. Whitespace inside quoted literals is preserved.
func rdfTripleFields(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "@") {
		return nil
	}
	line = strings.TrimSuffix(line, ".")

	var fields []string
	var b strings.Builder
	inQuote, escaped := false, false
	flush := func() {
		if b.Len() > 0 {
			fields = append(fields, b.String())
			b.Reset()
		}
	}
	for _, r := range line {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case inQuote && r == '\\':
			b.WriteRune(r)
			escaped = true
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return fields
}

// rdfEntityLabel turns a Turtle entity object into a normalized comparison key
// and a human-readable label. Quoted literals and generic values are skipped:
// only named entities should create note-to-note graph edges.
func rdfEntityLabel(object string) (key, label string) {
	label = strings.TrimSpace(object)
	if label == "" || strings.HasPrefix(label, `"`) || strings.HasPrefix(label, "[") || strings.HasPrefix(label, "_") {
		return "", ""
	}
	label = strings.Trim(label, `'`)
	label = strings.TrimPrefix(label, "n:")
	label = strings.Trim(label, "<>")
	label = strings.Join(strings.Fields(label), " ")
	if label == "" || len([]rune(label)) < 3 {
		return "", ""
	}

	key = strings.ToLower(label)
	switch key {
	case "this", "note", "self", "true", "false", "yes", "no", "none", "null":
		return "", ""
	}
	if strings.IndexFunc(key, func(r rune) bool { return !unicode.IsDigit(r) }) == -1 {
		return "", ""
	}
	return key, label
}

// rdfEntityLabels returns the shared-entity candidates of one note. Both the
// subject and the object are considered: notes that mention the same person,
// place, project or event become connected in the graph, regardless of whether
// that entity appears on the left or right side of a triple.
func rdfEntityLabels(rdf string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(rdf, "\n") {
		fields := rdfTripleFields(line)
		if len(fields) < 3 {
			continue
		}
		for _, term := range []string{fields[0], fields[2]} {
			key, label := rdfEntityLabel(term)
			if key == "" {
				continue
			}
			if _, seen := out[key]; !seen {
				out[key] = label
			}
		}
	}
	return out
}

type rdfPair struct{ a, b int64 }

// rebuildRDFLinks creates bidirectional note links for every pair of notes that
// share an RDF entity. Existing links from the pair process are preserved; only
// links previously created by this builder are refreshed.
func rebuildRDFLinks(db *sql.DB) (int, error) {
	affected := map[int64]bool{}

	// Remember which notes had old RDF-derived links so their links_md can be
	// rebuilt after the refresh.
	oldRows, err := db.Query(`SELECT src_id, dst_id FROM links WHERE reason LIKE ?`, rdfLinkReasonPrefix+"%")
	if err != nil {
		return 0, err
	}
	for oldRows.Next() {
		var src, dst int64
		if err := oldRows.Scan(&src, &dst); err != nil {
			oldRows.Close()
			return 0, err
		}
		affected[src] = true
		affected[dst] = true
	}
	oldRows.Close()
	if err := oldRows.Err(); err != nil {
		return 0, err
	}

	if _, err := db.Exec(`DELETE FROM links WHERE reason LIKE ?`, rdfLinkReasonPrefix+"%"); err != nil {
		return 0, err
	}

	type noteRDF struct {
		id  int64
		rdf string
	}
	rows, err := db.Query(`SELECT id, rdf FROM notes WHERE rdf<>'' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var notes []noteRDF
	for rows.Next() {
		var n noteRDF
		if err := rows.Scan(&n.id, &n.rdf); err != nil {
			rows.Close()
			return 0, err
		}
		notes = append(notes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	index := map[string]map[int64]string{}
	for _, n := range notes {
		for key, label := range rdfEntityLabels(n.rdf) {
			ids := index[key]
			if ids == nil {
				ids = map[int64]string{}
				index[key] = ids
			}
			if _, seen := ids[n.id]; !seen {
				ids[n.id] = label
			}
		}
	}

	// Collect all shared entities for each note pair, then emit one link per
	// pair with a compact human-readable reason.
	shared := map[rdfPair][]string{}
	addLabel := func(p rdfPair, label string) {
		for _, existing := range shared[p] {
			if strings.EqualFold(existing, label) {
				return
			}
		}
		if len(shared[p]) < 3 {
			shared[p] = append(shared[p], label)
		}
	}
	for _, ids := range index {
		list := make([]int64, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				addLabel(rdfPair{list[i], list[j]}, ids[list[i]])
			}
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	created := 0
	for pair, labels := range shared {
		sort.Strings(labels)
		reason := rdfLinkReasonPrefix + strings.Join(labels, ", ")
		for _, p := range [][2]int64{{pair.a, pair.b}, {pair.b, pair.a}} {
			res, err := tx.Exec(`INSERT OR IGNORE INTO links(src_id, dst_id, reason) VALUES(?,?,?)`, p[0], p[1], reason)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				created++
			}
			affected[p[0]] = true
		}
	}
	for id := range affected {
		if err := rebuildLinksMD(tx, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}
