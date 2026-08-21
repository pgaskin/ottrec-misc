// Command new-activity-names groups dataset versions by week and reports the
// activity names appearing for the first time, how many facilities they
// appeared at, and how often they appear in later weeks.
//
// Datasets are grouped into weeks by the version dates, not the schedule
// ranges, since the purpose of this is to find the city's typos, and to find
// things we can/should normalize.
//
// Names are grouped by the scraper's normalized name, with the raw labels
// listed under them. If -normalized is not specified, a group is shown whenever
// a new raw label is seen for the first time.
//
// A name which shows up at one facility for one week and never again is usually
// a typo or a one-off wording change. A name which shows up at several
// facilities and persists is usually a real new activity or wording change.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ottrec/website/pkg/ottrecdata"
	"github.com/ottrec/website/pkg/ottrecidx"
)

var (
	cachePath  = flag.String("cache", "/tmp/ottrec-data.db", "ottrecdata cache path")
	normalized = flag.Bool("normalized", false, "detect new names by the normalized name")
	facilities = flag.Bool("facilities", false, "list the facilities each new name appeared at")
	exportJSON = flag.String("export.json", "", "json export path")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	var (
		byWeek   = map[time.Time]map[string]*nameInfo{}
		versions int
		err      error
	)
	for ver, data := range eachVersion(ctx, *cachePath)(&err) {
		versions++

		week := weekStart(ver.Updated)
		names, ok := byWeek[week]
		if !ok {
			names = map[string]*nameInfo{}
			byWeek[week] = names
		}
		for act := range data.Activities() {
			label := strings.TrimSpace(act.GetLabel())
			name := cmp.Or(strings.TrimSpace(act.GetName()), label)
			if name == "" {
				continue
			}
			st, ok := names[name]
			if !ok {
				st = &nameInfo{Labels: map[string]struct{}{}, Facilities: map[string]struct{}{}}
				names[name] = st
			}
			st.Labels[cmp.Or(label, name)] = struct{}{}
			st.Facilities[act.Facility().GetName()] = struct{}{}
			st.Seen++
		}
	}
	if err != nil {
		panic(err)
	}
	if len(byWeek) == 0 {
		fmt.Fprintln(os.Stderr, "no versions in the cache")
		os.Exit(1)
	}

	weeks := slices.SortedFunc(maps.Keys(byWeek), func(a, b time.Time) int { return a.Compare(b) })
	news := analyze(weeks, byWeek, *normalized)

	fmt.Printf("%-10s  %-46s %5s %5s %7s %7s\n", "week", "activity name", "facs", "seen", "future", "fweeks")
	for _, n := range news {
		fmt.Printf("%-10s  %-46s %5d %5d %7d %7d\n", n.Week, truncate(n.Name, 46), len(n.Facilities), n.Seen, n.Future, n.FutureWks)
		for _, label := range n.Labels {
			if len(n.Labels) == 1 && strings.EqualFold(label, n.Name) {
				continue
			}
			mark := " "
			if slices.Contains(n.NewLabels, label) {
				mark = "+"
			}
			fmt.Printf("%12s%s %s\n", "", mark, label)
		}
		if *facilities {
			fmt.Printf("%12s  %s\n", "", strings.Join(n.Facilities, ", "))
		}
	}

	var oneOff int
	for _, n := range news {
		if n.Future == 0 && len(n.Facilities) == 1 {
			oneOff++
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d versions over %d weeks (%s to %s): %d new names, %d only seen once at a single facility\n",
		versions, len(weeks), weeks[0].Format(time.DateOnly), weeks[len(weeks)-1].Format(time.DateOnly), len(news), oneOff)

	if *exportJSON != "" {
		buf, err := json.MarshalIndent(news, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(*exportJSON, append(buf, '\n'), 0666); err != nil {
			panic(err)
		}
	}
}

// nameInfo contains info for a single normalized name in a week.
type nameInfo struct {
	Labels     map[string]struct{}
	Facilities map[string]struct{}
	Seen       int
}

// newName is an activity name group seen for the first time in a week.
type newName struct {
	Week       string   `json:"week"`
	Name       string   `json:"name"`       // normalized name for labels
	Labels     []string `json:"labels"`     // raw labels in the group that week
	NewLabels  []string `json:"newLabels"`  // ... seen for the first time
	Facilities []string `json:"facilities"` // ... seen at these facilities
	Seen       int      `json:"seen"`       // occurrences that week
	Future     int      `json:"future"`     // occurrences in later weeks
	FutureWks  int      `json:"futureWks"`  // later weeks it appears in
}

// analyze iterates over the weeks from oldest to newest, reporting each name
// group the first time it turns up, or whenever a new spelling of it does.
func analyze(weeks []time.Time, byWeek map[time.Time]map[string]*nameInfo, normalized bool) []newName {
	var (
		news      []newName
		seenName  = map[string]bool{}
		seenLabel = map[string]bool{}
	)
	for i, week := range weeks {
		for name, st := range byWeek[week] {
			var fresh []string
			for label := range st.Labels {
				if key := name + "\x00" + label; !seenLabel[key] {
					seenLabel[key] = true
					fresh = append(fresh, label)
				}
			}
			isNew := !seenName[name]
			seenName[name] = true

			if normalized && !isNew || !normalized && len(fresh) == 0 {
				continue
			}

			var future, futureWks int
			for _, later := range weeks[i+1:] {
				if st := byWeek[later][name]; st != nil {
					future += st.Seen
					futureWks++
				}
			}
			slices.Sort(fresh)
			news = append(news, newName{
				Week:       week.Format(time.DateOnly),
				Name:       name,
				Labels:     slices.Sorted(maps.Keys(st.Labels)),
				NewLabels:  fresh,
				Facilities: slices.Sorted(maps.Keys(st.Facilities)),
				Seen:       st.Seen,
				Future:     future,
				FutureWks:  futureWks,
			})
		}
	}
	slices.SortStableFunc(news, func(a, b newName) int {
		return cmp.Or(strings.Compare(a.Week, b.Week), strings.Compare(a.Name, b.Name))
	})
	return news
}

// eachVersion yields data in a cache from newest to oldest. Superseded
// revisions of an update are skipped.
func eachVersion(ctx context.Context, cachePath string) func(*error) iter.Seq2[ottrecdata.DataVersion, ottrecidx.DataRef] {
	return func(err *error) iter.Seq2[ottrecdata.DataVersion, ottrecidx.DataRef] {
		return func(yield func(ottrecdata.DataVersion, ottrecidx.DataRef) bool) {
			*err = func() error {
				cache, err := ottrecdata.OpenCacheReadOnly(cachePath)
				if err != nil {
					return fmt.Errorf("open cache %q: %w", cachePath, err)
				}
				defer cache.Close()

				dxr := new(ottrecidx.Indexer)
				seen := map[int64]bool{}
				for ver := range cache.DataVersions(ctx)(&err) {
					if seen[ver.Updated.UnixNano()] {
						continue // older revision
					}
					seen[ver.Updated.UnixNano()] = true

					var pbh string
					for hash, format := range cache.DataFormats(ctx, ver.ID)(&err) {
						if format == "pb" {
							pbh = hash
							break
						}
					}
					if err != nil {
						return fmt.Errorf("list %s formats: %w", ver.ID, err)
					}
					if pbh == "" {
						return fmt.Errorf("read %s: missing pb", ver.ID)
					}

					var pb []byte
					cache.ReadBlob(ctx, pbh, false, func(r io.Reader, i int64) (err error) {
						pb, err = io.ReadAll(r)
						return
					})
					if err != nil {
						return fmt.Errorf("read %s: %w", ver.ID, err)
					}

					idx, err := dxr.Load(pb)
					if err != nil {
						return fmt.Errorf("load %s: %w", ver.ID, err)
					}

					if !yield(ver, idx.Data()) {
						break
					}
				}
				if err != nil {
					return fmt.Errorf("list versions: %w", err)
				}
				return nil
			}()
		}
	}
}

// weekStart gets the monday of t's week.
func weekStart(t time.Time) time.Time {
	t = t.In(ottrecidx.TZ)
	y, m, d := t.Date()
	t = time.Date(y, m, d, 0, 0, 0, 0, ottrecidx.TZ)
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
