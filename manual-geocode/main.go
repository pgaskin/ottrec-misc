package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/pgaskin/ottrec-website/pkg/ottrecdl"
	"github.com/pgaskin/ottrec-website/pkg/ottrecidx"
)

func main() {
	ctx := context.Background()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	context.AfterFunc(ctx, func() {
		fmt.Fprintf(os.Stderr, "\ninterrupted\n")
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		<-ch
		os.Exit(1)
	})

	ocl := &ottrecdl.Client{
		Base: "https://data.ottrec.ca/",
	}

	pb, err := ocl.Latest(ctx, "pb")
	if err != nil {
		panic(err)
	}

	idx, err := new(ottrecidx.Indexer).Load(pb)
	if err != nil {
		panic(err)
	}

	ctx, cancel := chromedp.NewExecAllocator(ctx, slices.Concat(chromedp.DefaultExecAllocatorOptions[:], []chromedp.ExecAllocatorOption{
		chromedp.Flag("headless", false),
	})...)
	defer cancel()

	ctx, cancel = chromedp.NewContext(ctx)
	defer cancel()

	latLngCh := make(chan [2]float64)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Navigate("https://maps.google.com"), // note: enable satellite, globe view
		listenCopyLatLng(func(lat, lng float64) {
			select {
			case latLngCh <- [2]float64{lat, lng}:
			default:
			}
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			for fac := range idx.Data().Facilities() {
				addr := strings.ReplaceAll(strings.TrimSpace(fac.GetAddress()), "\n", ", ")
				fmt.Printf("// %s (%s)\n", fac.GetName(), addr)

				if err := search(ctx, addr); err != nil {
					return err
				}

				tmp, _, _ := strings.Cut(addr, ",")
				tmp = strings.TrimSpace(tmp)
				tmp = strings.TrimRightFunc(tmp, unicode.IsLower)
				latLng := <-latLngCh
				fmt.Printf("case strings.HasPrefix(addr, %q): return %.5f, %.5f, true\n", tmp, latLng[0], latLng[1])
			}
			return nil
		}),
	}); err != nil {
		panic(err)
	}
}

func listenCopyLatLng(fn func(lat, lng float64)) chromedp.Action {
	return chromedp.Tasks{
		chromedp.ActionFunc(func(ctx context.Context) error {
			latLngRe := regexp.MustCompile(`^(-?[0-9]+\.[0-9]+), (-?[0-9]+\.[0-9]+)$`)
			chromedp.ListenTarget(ctx, func(ev any) {
				switch ev := ev.(type) {
				case *runtime.EventBindingCalled:
					if ev.Name == "interceptClipboardWrite" {
						if m := latLngRe.FindStringSubmatch(ev.Payload); m != nil {
							lat, _ := strconv.ParseFloat(m[1], 64)
							lng, _ := strconv.ParseFloat(m[2], 64)
							if fn != nil {
								fn(lat, lng)
							}
						}
					}
				}
			})
			return nil
		}),
		runtime.AddBinding("interceptClipboardWrite"),
		chromedp.Evaluate(`navigator.clipboard.writeText = async text => globalThis.interceptClipboardWrite(text)`, nil),
	}
}

func search(ctx context.Context, q string) error {
	buf, _ := json.Marshal(q)
	return chromedp.Evaluate(`document.getElementById("searchboxinput").value = `+string(buf)+`; document.getElementById("searchbox-searchbutton").click()`, nil).Do(ctx)
}
