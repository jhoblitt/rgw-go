package citecheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// rePairRule is a lead-in's statement of the pair convention, in the
	// words the registries write it in: "each pair of lines is v19.2.6's,
	// then v20.2.4's", "a pair of line numbers is ...", "unless a pair is
	// given, ...", "lines are given as ...", and "cited at v19.2.6 and then
	// v20.2.4". Its two groups, or its third and fourth, are the tags in
	// order.
	rePairRule = regexp.MustCompile(`(?i)\b(?:(?:each|a) pair(?: of (?:lines|line numbers))?(?: below)? is|` +
		`a pair is given,|lines are given as) (` + tagForm + `)'s,? then (` + tagForm + `)'s|` +
		`\bcited at (` + tagForm + `),? and then (` + tagForm + `)\b`)
	// rePairSep is what stands between a pair's two citations.
	rePairSep = regexp.MustCompile("^[`\\s]*[;,][`\\s]*$")
)

// pairList is a lead-in that states the pair convention and the plain
// citations under it, which the convention gives their refs two by two when
// the list is regular: the first of each pair the first tag, the second the
// second.
type pairList struct {
	lead *paraScan
	tags [2]string
	// members are the plain citations under the lead-in, in order, and
	// second the ones that close a pair.
	members []*goItem
	second  map[*goItem]bool
	// why is why the convention is not applied to the list, or "".
	why string
}

// pairRule returns the tags a paragraph's statement of the pair convention
// names, in order, and where the statement stands in its norm.
func pairRule(ps *paraScan) (tags [2]string, lo, hi int, ok bool) {
	m := rePairRule.FindStringSubmatchIndex(ps.norm)
	if m == nil {
		return tags, 0, 0, false
	}
	g := 2
	if m[2] < 0 {
		g = 6
	}
	tags = [2]string{ps.norm[m[g]:m[g+1]], ps.norm[m[g+2]:m[g+3]]}
	return tags, m[0], m[1], true
}

// findPairLists finds the lead-ins that state the pair convention and the
// list of plain citations under each: those in the lead-in's own paragraph
// and in each paragraph it leads in to, as leadIns reads one, whose nearest
// lead-in that states the convention it is. It records each list's
// citations in pairOf, and why the convention is not applied to a list that
// is not regular.
func (d *mdDoc) findPairLists() {
	d.pairOf = map[*goItem]*pairList{}
	for gi, scans := range d.groups {
		lists := map[int]*pairList{}
		for pi, ps := range scans {
			if tags, lo, hi, ok := pairRule(ps); ok {
				pl := &pairList{lead: ps, tags: tags, second: map[*goItem]bool{}}
				pl.why = d.leadProblem(ps, tags, lo, hi)
				lists[pi] = pl
			}
		}
		if len(lists) == 0 {
			continue
		}
		for pi, ps := range scans {
			pl := lists[pi]
			for _, k := range d.leadIns(gi, pi) {
				if pl != nil {
					break
				}
				pl = lists[k]
			}
			if pl == nil {
				continue
			}
			if ps != pl.lead && pl.why == "" {
				pl.why = d.listProblem(ps, "its list", pl.tags, 0, 0)
			}
			for _, it := range ps.items {
				if it.kind == fileItem || it.kind == contItem {
					pl.members = append(pl.members, it)
					d.pairOf[it] = pl
				}
			}
		}
		for _, pl := range lists {
			if pl.why == "" {
				pl.why = d.nestedLead(gi, pl)
			}
			if pl.why == "" {
				pl.why = d.pairUp(pl)
			}
		}
	}
}

// nestedLead returns why a pair list is irregular when a paragraph of it
// leads in to a citation of it, as leadIns reads one, and names a release, a
// ref, or both tags at once, which the citations under it would read as
// theirs; or "".
func (d *mdDoc) nestedLead(gi int, pl *pairList) string {
	lead := d.at[pl.lead][1]
	for _, it := range pl.members {
		for _, k := range d.leadIns(gi, d.at[d.itemPara[it]][1]) {
			if k == lead {
				break
			}
			ps := d.groups[gi][k]
			if ws := releaseWords(ps, 0, len(ps.norm)); len(ws) > 0 {
				return fmt.Sprintf("%s in its list has a lead-in of its own that names %q", it.text,
					ps.norm[ws[0].at:ws[0].end])
			}
		}
	}
	return ""
}

// leadProblem returns why a lead-in that states the pair convention, at
// norm[lo:hi], does not give it: the statement names one tag twice, or the
// rest of the lead-in makes its list irregular, as listProblem finds; or "".
func (d *mdDoc) leadProblem(ps *paraScan, tags [2]string, lo, hi int) string {
	if tags[0] == tags[1] {
		return fmt.Sprintf("its statement names %s twice", tags[0])
	}
	return d.listProblem(ps, "its lead-in", tags, lo, hi)
}

// bothWords are the words that name a pair list's two tags at once.
var bothWords = []string{
	"both tags", "both releases", "either tag", "either release", "each tag", "each release",
	"every tag", "every release",
}

// listProblem returns why a paragraph of a pair list, outside the
// statement at norm[lo:hi], makes the list irregular, or "", where says
// which paragraph it is, the lead-in or one of its list: it holds a
// link, which carries its own ref; ties a release to a citation, right
// before it or by an "at" after it; names a release or a ref other than the
// list's two tags, a third; or names one of the two, or both at once, as
// "both tags" does, in the sentence of a citation in the list, which then
// reads as that citation's.
func (d *mdDoc) listProblem(ps *paraScan, where string, tags [2]string, lo, hi int) string {
	for _, it := range ps.items {
		if it.kind == linkItem {
			text := ps.para.text[it.start:it.end]
			if k := strings.Index(text, "]("); strings.HasPrefix(text, "[") && k > 0 {
				text = text[1:k]
			}
			return fmt.Sprintf("%s holds a link, %s, which carries its own ref", where, text)
		}
	}
	at := d.at[ps]
	row := d.blocks[at[0]][at[1]].kind == mdRow
	var other []string
	for _, w := range releaseWords(ps, 0, len(ps.norm)) {
		if lo <= w.at && w.end <= hi {
			continue
		}
		if w.owner >= 0 {
			for _, it := range ps.items {
				switch {
				case it.pos != w.owner:
				case w.at > it.pos:
					return fmt.Sprintf("%s in %s is cited at %s", it.text, where, w.name)
				default:
					return fmt.Sprintf("%s in %s has its own release, %s, right before it", it.text, where, w.name)
				}
			}
		}
		if !slices.Contains(tags[:], w.name) && !slices.Contains(bothWords, w.name) {
			if !slices.Contains(other, w.name) {
				other = append(other, w.name)
			}
			continue
		}
		slo, shi := sentence(ps.norm, w.at, row)
		for _, it := range ps.items {
			if (it.kind == fileItem || it.kind == contItem) && slo <= it.pos && it.pos < shi {
				return fmt.Sprintf("%s in %s shares its sentence with %q, which names a release", it.text, where,
					ps.norm[w.at:w.end])
			}
		}
	}
	if len(other) > 0 {
		return fmt.Sprintf("%s names %s, a release other than its two tags", where, strings.Join(other, " and "))
	}
	return ""
}

// pairUp splits a pair list's citations into its pairs and returns why the
// list is not regular, or "": a citation that carries its own marker or
// "at <ref>"; one that stands alone, or three or more run together, where
// each pair is two citations with a ";" or a "," alone between them and
// nothing like that joins it to the next; or a pair whose second citation
// names another file than its first.
func (d *mdDoc) pairUp(pl *pairList) string {
	for _, it := range pl.members {
		ps := d.paraOf(it)
		switch t, both := tagAfter(ps.norm, it.pos+1); {
		case it.mark != "":
			return fmt.Sprintf("%s in its list carries its own [%s] marker", it.text, it.mark)
		case both:
			return it.text + " in its list is cited at both tags"
		case t != "":
			return fmt.Sprintf("%s in its list is cited at %s", it.text, t)
		}
		if w := atOther(ps.norm, it.pos+1); w != "" {
			return fmt.Sprintf("%s in its list is cited %s", it.text, w)
		}
	}
	var run []*goItem
	closeRun := func() string {
		switch {
		case len(run) == 1:
			return run[0].text + " in its list stands alone, in no pair"
		case len(run) > 2:
			return fmt.Sprintf("%d citations in its list run together, %s to %s, which marks no pairs", len(run),
				run[0].text, run[len(run)-1].text)
		case len(run) == 2 && run[1].kind == fileItem && (run[0].kind != fileItem || run[1].name != run[0].name):
			return fmt.Sprintf("%s and %s in its list, a pair, name two files", run[0].text, run[1].text)
		case len(run) == 2:
			pl.second[run[1]] = true
		}
		return ""
	}
	for i, it := range pl.members {
		if i > 0 {
			prev := pl.members[i-1]
			ps := d.paraOf(it)
			if d.paraOf(prev) != ps || !rePairSep.MatchString(ps.norm[prev.pos+1:it.pos]) {
				if why := closeRun(); why != "" {
					return why
				}
				run = nil
			}
		}
		run = append(run, it)
	}
	return closeRun()
}

// paraOf returns the paragraph that holds a citation under a pair lead-in.
func (d *mdDoc) paraOf(it *goItem) *paraScan { return d.itemPara[it] }

// pairRef returns the ref the pair convention gives a plain citation, and
// whether it gives one: the citation is in a regular pair list.
func (d *mdDoc) pairRef(it *goItem) (string, bool) {
	pl := d.pairOf[it]
	if pl == nil || pl.why != "" {
		return "", false
	}
	it.rule, it.releases = "pair convention", pl.tags[:]
	if pl.second[it] {
		return pl.tags[1], true
	}
	return pl.tags[0], true
}

// pairWhy returns why a plain citation under the pair lead-in lead has no
// ref, when lead states the convention and its list is not regular, or "".
func (d *mdDoc) pairWhy(it *goItem, lead *paraScan) string {
	pl := d.pairOf[it]
	if pl == nil || pl.lead != lead || pl.why == "" {
		return ""
	}
	return pl.decline(pl.why)
}

// decline returns the reason a citation in a pair list gets when the
// convention is not applied to the list for why.
func (pl *pairList) decline(why string) string {
	return fmt.Sprintf(`no ref: its lead-in gives each pair of lines as %s's, then %s's, but %s, so the convention `+
		`is not applied to its list: write "at <tag>" after it`, pl.tags[0], pl.tags[1], why)
}

// settle returns, for a regular pair list whose citations have been
// resolved, why the convention is not applied to it after all, or "": a
// citation in it is not ceph's, does not resolve, the reason failed holds
// for it, has nothing beside it that names code to check its lines by, or
// cannot be written as a link where it stands, or a pair's two citations
// resolve to two files.
func (d *mdDoc) settle(pl *pairList, failed map[*goItem]string) string {
	for i, it := range pl.members {
		switch {
		case !it.ceph:
			return it.text + " in its list is not ceph's"
		case failed[it] != "":
			return it.text + " in its list does not resolve"
		case len(it.ids) == 0:
			return fmt.Sprintf("nothing beside %s in its list names code to check its lines by", it.text)
		case d.refusal(it) != "":
			return it.text + " in its list cannot be written as a link where it stands"
		case pl.second[it] && pl.members[i-1].path != it.path:
			return fmt.Sprintf("%s and %s in its list, a pair, resolve to two files, %s and %s", pl.members[i-1].text,
				it.text, pl.members[i-1].path, it.path)
		}
	}
	return ""
}

// refusal returns why a plain citation that resolves is not written as a
// link where it stands, as inPlace refuses one, or "".
func (d *mdDoc) refusal(it *goItem) string {
	if why, ok := d.noFix[it]; ok {
		return why
	}
	if p := d.itemPara[it].para; p.lineAt(it.end-1) != p.lineAt(it.start) {
		return d.broken(it)
	}
	return ""
}
