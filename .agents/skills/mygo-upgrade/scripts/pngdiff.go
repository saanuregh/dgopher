// Command pngdiff compares the PNGs of two folders of screenshots, as
// DGOPHER_SNAPSHOTS writes them. For each that changed it prints how many
// pixels changed, the largest change of a channel, and the box around the
// changes, and writes the "after" image dimmed with the changes in red into
// the third folder.
//
//	go run .agents/skills/mygo-upgrade/scripts/pngdiff.go BEFORE AFTER DIFF
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: pngdiff BEFORE AFTER DIFF")
		os.Exit(2)
	}
	before, after, out := os.Args[1], os.Args[2], os.Args[3]
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail(err)
	}
	names, err := filepath.Glob(filepath.Join(before, "*.png"))
	if err != nil {
		fail(err)
	}
	for _, name := range names {
		base := filepath.Base(name)
		a, b := load(name), load(filepath.Join(after, base))
		if a == nil || b == nil {
			fmt.Printf("%-40s missing on one side\n", base)
			continue
		}
		if a.Bounds() != b.Bounds() {
			fmt.Printf("%-40s size %v -> %v\n", base, a.Bounds().Size(), b.Bounds().Size())
			continue
		}
		r := a.Bounds()
		marked := image.NewRGBA(r)
		changed, most := 0, 0
		var box image.Rectangle
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				p := color.RGBAModel.Convert(a.At(x, y)).(color.RGBA)
				q := color.RGBAModel.Convert(b.At(x, y)).(color.RGBA)
				d := max(diff(p.R, q.R), diff(p.G, q.G), diff(p.B, q.B), diff(p.A, q.A))
				if d == 0 {
					gray := uint8((int(q.R) + int(q.G) + int(q.B)) / 9)
					marked.Set(x, y, color.RGBA{gray, gray, gray, 255})
					continue
				}
				changed++
				most = max(most, d)
				box = box.Union(image.Rect(x, y, x+1, y+1))
				marked.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
		if changed == 0 {
			continue
		}
		fmt.Printf("%-40s pixels=%-7d max=%-3d box=%v of %v\n", base, changed, most, box, r.Size())
		f, err := os.Create(filepath.Join(out, base))
		if err != nil {
			fail(err)
		}
		if err := png.Encode(f, marked); err != nil {
			fail(err)
		}
		f.Close()
	}
}

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fail(fmt.Errorf("%s: %w", path, err))
	}
	return img
}

func diff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "pngdiff:", err)
	os.Exit(1)
}
