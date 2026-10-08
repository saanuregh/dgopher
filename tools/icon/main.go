// Icon draws the app's icon, resources/icon.png:
//
//	go run ./tools/icon
package main

import (
	_ "embed"
	"image/png"
	"log"
	"os"

	"github.com/egoist/mygo/ui"
)

// The Go gopher climbing out of a database, as DBeaver's beaver is its
// namesake.
//
//go:embed gopher.svg
var gopherSVG []byte

func main() {
	art := ui.MustParseSVG(gopherSVG)
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Box(c).Fill().Padding(100).Children(func() {
			ui.Box(c).Fill().Radius(180).
				LinearGradient(ui.LinearGradient{From: ui.Hex("#1e4b7a"), To: ui.Hex("#0b1b33"), Angle: 135}).
				Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.25)).
				Children(func() {
					ui.Image(c, art).Fill()
				})
		})
	}, 1024, 1024, 1)
	f, err := os.Create("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}
