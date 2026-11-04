package index

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// Search returns the packages matching every word of query, best match first.
func (idx *Index) Search(query string) []*Package {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		pkg   *Package
		score int
	}
	var hits []hit
	for i := range idx.Packages {
		pkg := &idx.Packages[i]
		id, name, desc := strings.ToLower(pkg.ID), strings.ToLower(pkg.Name), strings.ToLower(pkg.Description)
		nameWords := splitWords(name)
		total := 0
		for _, word := range words {
			s := wordScore(word, id, name, desc, nameWords)
			if s == 0 {
				total = 0
				break
			}
			total += s
		}
		if total > 0 {
			hits = append(hits, hit{pkg, total})
		}
	}
	slices.SortFunc(hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.pkg.ID, b.pkg.ID))
	})
	out := make([]*Package, len(hits))
	for i, h := range hits {
		out[i] = h.pkg
	}
	return out
}

func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func wordScore(word, id, name, desc string, nameWords []string) int {
	switch {
	case id == word:
		return 100
	case name == word:
		return 90
	case strings.HasPrefix(id, word):
		return 80
	case slices.ContainsFunc(nameWords, func(w string) bool { return strings.HasPrefix(w, word) }):
		return 60
	case strings.Contains(id, word):
		return 50
	case strings.Contains(name, word):
		return 40
	case strings.Contains(desc, word):
		return 20
	case isSubsequence(word, id):
		return 10
	case closeMatch(word, id) || slices.ContainsFunc(nameWords, func(w string) bool { return closeMatch(word, w) }):
		return 5
	}
	return 0
}

// isSubsequence catches abbreviations such as "npp" for "notepadplusplus".
func isSubsequence(word, s string) bool {
	if len(word) < 3 {
		return false
	}
	for _, r := range s {
		if strings.HasPrefix(word, string(r)) {
			word = word[len(string(r)):]
			if word == "" {
				return true
			}
		}
	}
	return false
}

// closeMatch catches typos: one edit in a short word, two in a longer one.
func closeMatch(word, s string) bool {
	if len(word) < 3 {
		return false
	}
	limit := 1
	if len(word) > 5 {
		limit = 2
	}
	return editDistance(word, s, limit) <= limit
}

// editDistance counts insertions, deletions, substitutions and swaps of
// neighbouring letters, giving up with limit+1 when the lengths rule it out.
func editDistance(a, b string, limit int) int {
	x, y := []rune(a), []rune(b)
	if d := len(x) - len(y); d > limit || -d > limit {
		return limit + 1
	}
	before, prev := make([]int, len(y)+1), make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				cur[j] = min(cur[j], before[j-2]+1)
			}
		}
		before, prev, cur = prev, cur, before
	}
	return prev[len(y)]
}
