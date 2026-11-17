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

// rdfPrefix is emitted when facts stored in the normalized tables are rendered
// back to Turtle.
const rdfPrefix = "@prefix n: <note:> ."

// rdfEmptyPredicate marks a note that produced no usable triples. The model
// occasionally answers with only a prefix declaration or with text that cannot
// be parsed into triples; such notes would otherwise stay without rows in
// note_facts and be reprocessed on every run. The marker is a literal fact, so
// it never connects notes in the graph, and it is hidden when facts are
// rendered back to Turtle.
const rdfEmptyPredicate = "has_no_facts"

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

// rdfTriple is one parsed (subject, predicate, object) line.
type rdfTriple struct {
	Subject   string
	Predicate string
	Object    string
}

// parseRDFTriples extracts simple Turtle triples from a model answer. Prefix
// declarations, comments and incomplete lines are ignored.
func parseRDFTriples(rdf string) []rdfTriple {
	var out []rdfTriple
	for _, line := range strings.Split(rdf, "\n") {
		fields := rdfTripleFields(line)
		if len(fields) < 3 {
			continue
		}
		out = append(out, rdfTriple{
			Subject:   fields[0],
			Predicate: fields[1],
			Object:    strings.Join(fields[2:], " "),
		})
	}
	return out
}

// rdfTerm is the normalized form of one subject/object used by the entities
// table. Literal values are kept verbatim (including their quotes) so the
// original triple can be rendered again. Only non-literal, meaningful entities
// are marked as graph links.
type rdfTerm struct {
	Name    string
	Key     string
	Literal bool
	Link    bool
}

// parseRDFTerm normalizes one subject or object term.
func parseRDFTerm(term string) (rdfTerm, bool) {
	term = strings.TrimSpace(term)
	if term == "" {
		return rdfTerm{}, false
	}
	if strings.HasPrefix(term, `"`) || strings.HasPrefix(term, `'`) {
		return rdfTerm{Name: term, Key: strings.ToLower(term), Literal: true}, true
	}
	if key, label := rdfEntityLabel(term); key != "" {
		return rdfTerm{Name: label, Key: key, Link: true}, true
	}
	// Keep generic terms (n:this, short names, blank nodes…) as entities too,
	// but never let them connect notes in the graph.
	label := strings.Trim(term, `'`)
	label = strings.TrimPrefix(label, "n:")
	label = strings.Trim(label, "<>")
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		return rdfTerm{}, false
	}
	return rdfTerm{Name: label, Key: strings.ToLower(label)}, true
}

// rdfPredicateName normalizes a predicate term, dropping the n: prefix.
func rdfPredicateName(term string) string {
	term = strings.TrimSpace(term)
	term = strings.TrimPrefix(term, "n:")
	term = strings.Trim(term, "<>")
	term = strings.Trim(term, `"'`)
	return strings.Join(strings.Fields(term), " ")
}

// rdfTermText renders a stored subject/object back to Turtle.
func rdfTermText(name string, literal bool) string {
	if literal {
		return name
	}
	return "n:" + name
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// upsertEntity returns the id of an entity, inserting it when needed.
func upsertEntity(e execer, t rdfTerm) (int64, error) {
	if _, err := e.Exec(`INSERT OR IGNORE INTO entities(name, key, literal, link) VALUES(?,?,?,?)`,
		t.Name, t.Key, boolInt(t.Literal), boolInt(t.Link)); err != nil {
		return 0, err
	}
	var id int64
	err := e.QueryRow(`SELECT id FROM entities WHERE key=? AND literal=?`, t.Key, boolInt(t.Literal)).Scan(&id)
	return id, err
}

// upsertPredicate returns the id of a predicate, inserting it when needed.
func upsertPredicate(e execer, name string) (int64, error) {
	if _, err := e.Exec(`INSERT OR IGNORE INTO predicates(name) VALUES(?)`, name); err != nil {
		return 0, err
	}
	var id int64
	err := e.QueryRow(`SELECT id FROM predicates WHERE name=?`, name).Scan(&id)
	return id, err
}

// upsertFact returns the id of a triple, inserting it when needed. Facts are
// shared between notes that contain the same triple.
func upsertFact(e execer, subjectID, predicateID, objectID int64) (int64, error) {
	if _, err := e.Exec(`INSERT OR IGNORE INTO facts(subject_id, predicate_id, object_id) VALUES(?,?,?)`,
		subjectID, predicateID, objectID); err != nil {
		return 0, err
	}
	var id int64
	err := e.QueryRow(`SELECT id FROM facts WHERE subject_id=? AND predicate_id=? AND object_id=?`,
		subjectID, predicateID, objectID).Scan(&id)
	return id, err
}

// pruneRDF removes facts that no note references any more, then removes any
// entities and predicates left without a fact.
func pruneRDF(e execer) error {
	if _, err := e.Exec(`DELETE FROM facts WHERE id NOT IN (SELECT fact_id FROM note_facts)`); err != nil {
		return err
	}
	if _, err := e.Exec(`DELETE FROM entities WHERE id NOT IN (
		SELECT subject_id FROM facts UNION SELECT object_id FROM facts)`); err != nil {
		return err
	}
	_, err := e.Exec(`DELETE FROM predicates WHERE id NOT IN (SELECT predicate_id FROM facts)`)
	return err
}

// storeNoteRDF replaces the facts of one note with the triples parsed from rdf.
// Entities, predicates and facts are reused across notes; note_facts is the
// many-to-many list of fact ids belonging to a note.
func storeNoteRDF(db *sql.DB, noteID int64, rdf string) error {
	triples := parseRDFTriples(rdf)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM note_facts WHERE note_id=?`, noteID); err != nil {
		return err
	}
	stored := 0
	for i, triple := range triples {
		subject, ok := parseRDFTerm(triple.Subject)
		if !ok {
			continue
		}
		object, ok := parseRDFTerm(triple.Object)
		if !ok {
			continue
		}
		predicateName := rdfPredicateName(triple.Predicate)
		if predicateName == "" {
			continue
		}

		subjectID, err := upsertEntity(tx, subject)
		if err != nil {
			return err
		}
		predicateID, err := upsertPredicate(tx, predicateName)
		if err != nil {
			return err
		}
		objectID, err := upsertEntity(tx, object)
		if err != nil {
			return err
		}
		factID, err := upsertFact(tx, subjectID, predicateID, objectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO note_facts(note_id, fact_id, position) VALUES(?,?,?)`,
			noteID, factID, i); err != nil {
			return err
		}
		stored++
	}
	// A note the model could not turn into any triple still gets an empty
	// marker fact, so the RDF process treats it as done instead of retrying it
	// on every run.
	if stored == 0 {
		subject := rdfTerm{Name: "this", Key: "this"}
		object := rdfTerm{Name: `"true"`, Key: `"true"`, Literal: true}
		subjectID, err := upsertEntity(tx, subject)
		if err != nil {
			return err
		}
		predicateID, err := upsertPredicate(tx, rdfEmptyPredicate)
		if err != nil {
			return err
		}
		objectID, err := upsertEntity(tx, object)
		if err != nil {
			return err
		}
		factID, err := upsertFact(tx, subjectID, predicateID, objectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO note_facts(note_id, fact_id, position) VALUES(?,?,0)`,
			noteID, factID); err != nil {
			return err
		}
	}
	if err := pruneRDF(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// rdfForNote renders the facts of one note back to the simple Turtle dialect
// used by the RDF prompt. Notes without facts produce an empty string.
func rdfForNote(db *sql.DB, noteID int64) string {
	rows, err := db.Query(`
		SELECT s.name, s.literal, p.name, o.name, o.literal
		FROM note_facts nf
		JOIN facts f ON f.id=nf.fact_id
		JOIN entities s ON s.id=f.subject_id
		JOIN predicates p ON p.id=f.predicate_id
		JOIN entities o ON o.id=f.object_id
		WHERE nf.note_id=?
		ORDER BY nf.position, f.id`, noteID)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var subject, predicate, object string
		var subjectLiteral, objectLiteral int
		if err := rows.Scan(&subject, &subjectLiteral, &predicate, &object, &objectLiteral); err != nil {
			return ""
		}
		// Hide the empty marker: it exists only to mark the note as processed.
		if predicate == rdfEmptyPredicate {
			continue
		}
		lines = append(lines, rdfTermText(subject, subjectLiteral != 0)+" n:"+predicate+" "+
			rdfTermText(object, objectLiteral != 0)+" .")
	}
	if err := rows.Err(); err != nil || len(lines) == 0 {
		return ""
	}
	return rdfPrefix + "\n" + strings.Join(lines, "\n")
}

// rdfRelatedLabel is one shared term (an entity or a predicate) that connects a
// note with another note.
type rdfRelatedLabel struct {
	kind  string // "entity" or "predicate"
	label string
}

// rdfRelatedNotes returns, for noteID, the other notes that share an entity or a
// predicate, keyed by the other note's id. Relations are derived directly from
// the normalized fact tables, so the RDF export never depends on the links
// written by the pair process. Generic terms such as n:this and quoted literals
// have link=0 and therefore never connect notes.
func rdfRelatedNotes(db *sql.DB, noteID int64) (map[int64][]rdfRelatedLabel, error) {
	out := map[int64][]rdfRelatedLabel{}

	collect := func(query, kind string) error {
		rows, err := db.Query(query, noteID, noteID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var other int64
			var label string
			if err := rows.Scan(&other, &label); err != nil {
				return err
			}
			// The empty marker is not a real predicate and must not connect
			// notes.
			if kind == "predicate" && label == rdfEmptyPredicate {
				continue
			}
			out[other] = append(out[other], rdfRelatedLabel{kind: kind, label: label})
		}
		return rows.Err()
	}

	// Notes sharing a named entity, whether it appears as a subject or object.
	if err := collect(`
		SELECT DISTINCT nf2.note_id, e.name
		FROM note_facts nf1
		JOIN facts f1 ON f1.id = nf1.fact_id
		JOIN entities e ON e.id IN (f1.subject_id, f1.object_id)
		JOIN facts f2 ON e.id IN (f2.subject_id, f2.object_id)
		JOIN note_facts nf2 ON nf2.fact_id = f2.id
		WHERE nf1.note_id = ? AND e.link = 1 AND nf2.note_id <> ?`, "entity"); err != nil {
		return nil, err
	}

	// Notes sharing a predicate.
	if err := collect(`
		SELECT DISTINCT nf2.note_id, p.name
		FROM note_facts nf1
		JOIN facts f1 ON f1.id = nf1.fact_id
		JOIN predicates p ON p.id = f1.predicate_id
		JOIN facts f2 ON f2.predicate_id = p.id
		JOIN note_facts nf2 ON nf2.fact_id = f2.id
		WHERE nf1.note_id = ? AND nf2.note_id <> ?`, "predicate"); err != nil {
		return nil, err
	}

	return out, nil
}

// rdfRelatedReason builds the human-readable reason shown next to a related
// note, listing the shared entities and predicates.
func rdfRelatedReason(labels []rdfRelatedLabel) string {
	var entities, predicates []string
	seen := map[string]bool{}
	for _, l := range labels {
		key := l.kind + "\x00" + strings.ToLower(l.label)
		if seen[key] {
			continue
		}
		seen[key] = true
		if l.kind == "predicate" {
			predicates = append(predicates, l.label)
		} else {
			entities = append(entities, l.label)
		}
	}
	sort.Strings(entities)
	sort.Strings(predicates)

	var parts []string
	if len(entities) > 0 {
		parts = append(parts, "entity "+strings.Join(entities, ", "))
	}
	if len(predicates) > 0 {
		parts = append(parts, "predicate "+strings.Join(predicates, ", "))
	}
	return rdfLinkReasonPrefix + strings.Join(parts, "; ")
}

// rdfRelatedLines returns the "[[note]] — reason" lines for the notes that
// share an entity or predicate with noteID, ordered by note id. Each shared
// relation is emitted for both notes, so the links are bidirectional.
func rdfRelatedLines(db *sql.DB, noteID int64) []string {
	related, err := rdfRelatedNotes(db, noteID)
	if err != nil || len(related) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(related))
	for id := range related {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var lines []string
	for _, id := range ids {
		var name string
		if err := db.QueryRow(`SELECT file_name FROM notes WHERE id=?`, id).Scan(&name); err != nil {
			continue
		}
		lines = append(lines, "- [["+noteLinkName(name)+"]] — "+rdfRelatedReason(related[id]))
	}
	return lines
}

// relatedSection wraps link lines in the shared "## Related notes" heading. It
// returns an empty string for an empty list.
func relatedSection(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "\n## Related notes\n" + strings.Join(lines, "\n") + "\n"
}

// rdfRelatedBlock renders the "## Related notes" section for one note from the
// entities and predicates it shares with other notes.
func rdfRelatedBlock(db *sql.DB, noteID int64) string {
	return relatedSection(rdfRelatedLines(db, noteID))
}

// linkTarget extracts the note name from a "[[" link "]]" line, lower-cased for
// comparison. It returns "" when the line carries no link.
func linkTarget(line string) string {
	i := strings.Index(line, "[[")
	if i < 0 {
		return ""
	}
	rest := line[i+2:]
	j := strings.Index(rest, "]]")
	if j < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(rest[:j]))
}

// combinedRelatedBlock merges the semantic pair links (linksMD, the note's
// stored links_md) with the RDF-derived lines into a single "## Related notes"
// section, de-duplicated by target note. Pair-process lines win when a note is
// linked by both sources.
func combinedRelatedBlock(linksMD string, rdfLines []string) string {
	seen := map[string]bool{}
	var lines []string
	add := func(line string) {
		if line = strings.TrimSpace(line); line == "" {
			return
		}
		if t := linkTarget(line); t != "" {
			if seen[t] {
				return
			}
			seen[t] = true
		}
		lines = append(lines, line)
	}
	for _, l := range strings.Split(linksMD, "\n") {
		add(l)
	}
	for _, l := range rdfLines {
		add(l)
	}
	return relatedSection(lines)
}

// migrateRDFToTables moves RDF generated by older versions from the legacy
// notes.rdf column and from the process_output table into the normalized fact
// tables, then removes the old column and rows.
func migrateRDFToTables(db *sql.DB) {
	if hasColumn(db, "notes", "rdf") {
		type legacyNote struct {
			id  int64
			rdf string
		}
		rows, err := db.Query(`SELECT id, rdf FROM notes WHERE TRIM(rdf)<>''`)
		if err == nil {
			var recs []legacyNote
			for rows.Next() {
				var rec legacyNote
				if rows.Scan(&rec.id, &rec.rdf) == nil {
					recs = append(recs, rec)
				}
			}
			rows.Close()
			for _, rec := range recs {
				var facts int
				_ = db.QueryRow(`SELECT COUNT(*) FROM note_facts WHERE note_id=?`, rec.id).Scan(&facts)
				if facts == 0 {
					_ = storeNoteRDF(db, rec.id, rec.rdf)
				}
			}
		}
		// Clear the old text before dropping the column, so a failed DROP
		// cannot re-import stale data on the next start.
		_, _ = db.Exec(`UPDATE notes SET rdf=''`)
		_, _ = db.Exec(`ALTER TABLE notes DROP COLUMN rdf`)
	}

	type legacyOutput struct {
		noteID int64
		output string
	}
	rows, err := db.Query(`SELECT note_id, output FROM process_output WHERE process_name=?`, RDFProcess)
	if err == nil {
		var recs []legacyOutput
		for rows.Next() {
			var rec legacyOutput
			if rows.Scan(&rec.noteID, &rec.output) == nil {
				recs = append(recs, rec)
			}
		}
		rows.Close()
		for _, rec := range recs {
			var facts int
			_ = db.QueryRow(`SELECT COUNT(*) FROM note_facts WHERE note_id=?`, rec.noteID).Scan(&facts)
			if facts == 0 {
				_ = storeNoteRDF(db, rec.noteID, rec.output)
			}
		}
	}
	_, _ = db.Exec(`DELETE FROM process_output WHERE process_name=?`, RDFProcess)
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

	// An entity connects notes whether it appears as a subject or as an object.
	// Literals and generic terms such as n:this have link=0.
	rows, err := db.Query(`
		SELECT e.key, e.name, nf.note_id
		FROM entities e
		JOIN facts f ON f.subject_id=e.id
		JOIN note_facts nf ON nf.fact_id=f.id
		WHERE e.link=1
		UNION
		SELECT e.key, e.name, nf.note_id
		FROM entities e
		JOIN facts f ON f.object_id=e.id
		JOIN note_facts nf ON nf.fact_id=f.id
		WHERE e.link=1
		ORDER BY 1, 3`)
	if err != nil {
		return 0, err
	}
	index := map[string]map[int64]string{}
	for rows.Next() {
		var key, label string
		var id int64
		if err := rows.Scan(&key, &label, &id); err != nil {
			rows.Close()
			return 0, err
		}
		ids := index[key]
		if ids == nil {
			ids = map[int64]string{}
			index[key] = ids
		}
		if _, seen := ids[id]; !seen {
			ids[id] = label
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
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
