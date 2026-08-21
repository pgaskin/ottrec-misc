// Command mso-pastes scans the cache for Microsoft Word paste junk in facility
// schedule sections, finding the date each paste appeared, how much of the
// section it covers, and the text which was pasted.
//
// Drupal strips the obvious Word markers (MsoNormal, mso-* styles, o:p
// elements), but leaves the lang and dir attributes Word puts on inline spans,
// which isn't used by anything else in the schedule HTML.
//
// They first started appearing in 2026. Note that this has false negatives; they
// sometimes paste, but filtered.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/ottrec/misc/internal/gitsh"
	"golang.org/x/net/html"
)

var (
	CacheDir = flag.String("cache.dir", "cache", "ottrec-data cache repo/worktree")
	CacheRev = flag.String("cache.rev", "HEAD", "last commit")
	CacheCat = flag.String("cache.cat", "facility,page", "cache categories to scan")

	Parallel  = flag.Int("p", runtime.NumCPU(), "number of commits to scan in parallel")
	Threshold = flag.Int("threshold", 3, "minimum increase in pasted elements for a commit to count as a new paste")

	Text      = flag.Bool("text", true, "print the pasted text")
	TextLimit = flag.Int("text.limit", 400, "truncate the printed pasted text (0 for no limit)")

	Export = flag.String("export", "", "json export path")
)

func main() {
	flag.Parse()

	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type commit struct {
	Hash string
	Date string // YYYY-MM-DD
}

type paste struct {
	Date     string   `json:"date"`
	Commit   string   `json:"commit"`
	Facility string   `json:"facility"`
	URL      string   `json:"url"`
	Was      int      `json:"was"`     // pasted elements before
	Now      int      `json:"now"`     // pasted elements after
	Covered  int      `json:"covered"` // characters inside pasted elements
	Total    int      `json:"total"`   // characters in the schedule sections
	Text     []string `json:"text"`    // the newly pasted text
}

type pasteStats struct {
	URL     string
	Total   int      // characters of schedule section text
	Covered int      // characters inside pasted elements
	Count   int      // number of outermost pasted elements
	Text    []string // text of the pasted elements
}

func run(ctx context.Context) error {
	var prefixes []string
	for prefix := range strings.SplitSeq(*CacheCat, ",") {
		prefixes = append(prefixes, prefix+"-")
	}

	var (
		commits []commit
		err     error
	)
	for hash, date := range gitsh.CommitsAscFirstParent(ctx, *CacheDir, *CacheRev)(&err) {
		// the ottrec-data commits are all UTC, so this matches git log --date=short
		commits = append(commits, commit{Hash: hash, Date: date.UTC().Format(time.DateOnly)})
	}
	if err != nil {
		return fmt.Errorf("list commits: %w", err)
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits in %s", *CacheDir)
	}
	slog.Info("scanning cache history", "repo", *CacheDir, "rev", *CacheRev, "commits", len(commits), "workers", *Parallel)

	scanned := make([]map[string]*pasteStats, len(commits))
	if err := forEachParallel(len(commits), *Parallel, func(i int) error {
		stats, err := scanCommit(ctx, *CacheDir, commits[i].Hash, prefixes)
		if err != nil {
			return fmt.Errorf("scan commit %s: %w", commits[i].Hash, err)
		}
		scanned[i] = stats
		return nil
	}); err != nil {
		return err
	}

	var (
		pastes []paste
		prev   = map[string]*pasteStats{}
	)
	for i, commit := range commits {
		for slug, cur := range scanned[i] {
			was := 0
			var wasText []string
			if old, ok := prev[slug]; ok {
				was, wasText = old.Count, old.Text
			}
			if cur.Count-was < *Threshold {
				continue
			}
			pastes = append(pastes, paste{
				Date:     commit.Date,
				Commit:   commit.Hash,
				Facility: slug,
				URL:      cur.URL,
				Was:      was,
				Now:      cur.Count,
				Covered:  cur.Covered,
				Total:    cur.Total,
				Text:     addedText(wasText, cur.Text),
			})
		}
		maps.Copy(prev, scanned[i])
	}
	slices.SortStableFunc(pastes, func(a, b paste) int {
		return strings.Compare(a.Date, b.Date)
	})

	for _, p := range pastes {
		pct := 0.0
		if p.Total != 0 {
			pct = float64(p.Covered) / float64(p.Total) * 100
		}
		fmt.Printf("%s %s %-40s %4d -> %-4d %5d/%-5d chars (%.1f%%)\n",
			p.Date, p.Commit[:8], p.Facility, p.Was, p.Now, p.Covered, p.Total, pct)
		if *Text {
			if s := formatText(p.Text, *TextLimit); s != "" {
				fmt.Printf("    %s\n", s)
			}
		}
	}

	facilities := map[string]struct{}{}
	for _, p := range pastes {
		facilities[p.Facility] = struct{}{}
	}
	fmt.Printf("\n%d pastes across %d facilities in %d commits (%s to %s)\n",
		len(pastes), len(facilities), len(commits),
		commits[0].Date, commits[len(commits)-1].Date)

	if *Export != "" {
		buf, err := json.MarshalIndent(pastes, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		if err := os.WriteFile(*Export, append(buf, '\n'), 0666); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		slog.Info("wrote json", "path", *Export, "pastes", len(pastes))
	}
	return nil
}

// scanCommit cached pages in a commit, keyed by the last path segment of the
// page's url.
func scanCommit(ctx context.Context, repo, commit string, prefixes []string) (map[string]*pasteStats, error) {
	names, err := gitLsTree(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	names = slices.DeleteFunc(names, func(name string) bool {
		return !slices.ContainsFunc(prefixes, func(prefix string) bool {
			return prefix != "" && strings.HasPrefix(name, prefix)
		})
	})
	if len(names) == 0 {
		return nil, nil
	}

	blobs, err := gitsh.CatFileBatch(ctx, repo, commit, names)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]*pasteStats, len(names))
	for _, buf := range blobs {
		if len(buf) == 0 {
			continue
		}
		u, doc, err := parseCachedPage(buf)
		if err != nil {
			continue // invalid
		}
		st := scanPage(doc)
		if st == nil {
			continue // no schedules
		}
		st.URL = u.String()
		slug := u.Path
		if _, x, ok := strings.Cut(slug, "place-listing/"); ok {
			slug = x
		}
		stats[strings.Trim(slug, "/")] = st
	}
	return stats, nil
}

func scanPage(doc *goquery.Document) *pasteStats {
	var st pasteStats
	for _, sec := range scheduleSections(doc) {
		st.Total += elidedLen(sec)
		for _, el := range sec.Find("span").EachIter() {
			if !isWordPasteSpan(el) {
				continue
			}
			// only count the outermost element of a nested run
			if el.ParentsUntilSelection(sec).FilterFunction(func(_ int, s *goquery.Selection) bool {
				return isWordPasteSpan(s)
			}).Length() != 0 {
				continue
			}
			st.Count++
			st.Covered += elidedLen(el)
			if s := elideSpaces(el.Text()); s != "" {
				st.Text = append(st.Text, s)
			}
		}
	}
	if st.Total == 0 {
		return nil
	}
	return &st
}

// scheduleSections finds the blocks holding schedule tables, so the caption
// and any surrounding notices are measured along with the table itself.
func scheduleSections(doc *goquery.Document) []*goquery.Selection {
	var (
		secs []*goquery.Selection
		seen = map[*html.Node]bool{}
	)
	for _, table := range doc.Find("table").EachIter() {
		sec := table.Closest(".paragraph--type--table")
		if sec.Length() == 0 {
			sec = table.Closest(".field__item")
		}
		if sec.Length() == 0 {
			sec = table
		}
		if node := sec.Nodes[0]; !seen[node] {
			seen[node] = true
			secs = append(secs, sec)
		}
	}
	return secs
}

var pasteLangRe = regexp.MustCompile(`(?i)^(en|fr)-`)

func isWordPasteSpan(s *goquery.Selection) bool {
	if !s.Is("span") {
		return false
	}
	if v, ok := s.Attr("lang"); ok && pasteLangRe.MatchString(v) {
		return true
	}
	_, ok := s.Attr("dir")
	return ok
}

func parseCachedPage(buf []byte) (*url.URL, *goquery.Document, error) {
	r := bufio.NewReader(bytes.NewReader(buf))

	req, err := http.ReadRequest(r)
	if err != nil {
		return nil, nil, fmt.Errorf("read cached request: %w", err)
	}
	req.URL.Scheme = "https"
	req.URL.Host = req.Host

	resp, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, nil, fmt.Errorf("read cached response: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("parse cached response: %w", err)
	}
	return req.URL, doc, nil
}

func elidedLen(s *goquery.Selection) int {
	return len(elideSpaces(s.Text()))
}

func elideSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func addedText(before, now []string) []string {
	was := make(map[string]int, len(before))
	for _, s := range before {
		was[s]++
	}
	var added []string
	for _, s := range now {
		if was[s] > 0 {
			was[s]--
			continue
		}
		added = append(added, s)
	}
	return added
}

func formatText(text []string, limit int) string {
	var b strings.Builder
	for _, s := range text {
		if s == "" {
			continue
		}
		if b.Len() != 0 {
			b.WriteString(" | ")
		}
		b.WriteString(strconv.Quote(s))
		if limit > 0 && b.Len() > limit {
			return b.String()[:limit] + "..."
		}
	}
	return b.String()
}

// forEachParallel calls fn for each index in [0,n), running up to workers at a
// time, returning the first error.
func forEachParallel(n, workers int, fn func(i int) error) error {
	workers = max(1, min(workers, n))

	var (
		wg   sync.WaitGroup
		idx  = make(chan int)
		errs = make([]error, workers)
	)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if err := fn(i); err != nil {
					errs[w] = err
					return
				}
			}
		}()
	}
	for i := range n {
		idx <- i
	}
	close(idx)
	wg.Wait()

	return errors.Join(errs...)
}

// gitLsTree lists the file names at the root of a commit's tree.
func gitLsTree(ctx context.Context, repo, commit string) ([]string, error) {
	var names []string
	if err := gitsh.Exec(ctx, repo, func(lines iter.Seq[string]) {
		for line := range lines {
			if line = strings.TrimSpace(line); line != "" {
				names = append(names, line)
			}
		}
	}, "ls-tree", "--name-only", "--end-of-options", commit); err != nil {
		return nil, err
	}
	return names, nil
}
