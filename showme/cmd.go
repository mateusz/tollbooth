package main

import (
	"image"
	"image/color"
	"log"
	"net/rpc"
	"sort"
	"time"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"github.com/hajimehoshi/ebiten"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/mateusz/tempomat/api"
	"golang.org/x/image/draw"
	"golang.org/x/image/font/gofont/gosmallcaps"
)

const (
	tickPeriod = 100 * time.Millisecond
	maxAge     = 10 * time.Second
)

var (
	imgPipe        chan *ebiten.Image
	backImg        *ebiten.Image
	palette        []colorful.Color
	palPointer     int
	palMap         map[string]colorful.Color
	scrWidth       int
	scrHeight      int
	tempomatClient *rpc.Client
	labels         map[string]time.Time
	fontFace       *truetype.Font
	buf            *image.RGBA
	labelBuf       *image.RGBA
	fontCtx        *freetype.Context
	lastTick       time.Time
)

func main() {
	scrWidth = 1350
	scrHeight = 500
	imgPipe = make(chan *ebiten.Image)
	labels = make(map[string]time.Time, 0)

	var err error
	backImg, err = ebiten.NewImage(scrWidth, scrHeight, ebiten.FilterNearest)
	if err != nil {
		log.Fatalf("%s", err)
	}

	fontFace, err = truetype.Parse(gosmallcaps.TTF)
	if err != nil {
		log.Fatalf("%s", err)
	}

	palette, err = colorful.SoftPaletteEx(10, colorful.SoftPaletteSettings{isNice, 50, true})
	if err != nil {
		log.Fatalf("%s", err)
	}

	palMap = make(map[string]colorful.Color, 0)
	palPointer = 0

	tempomatClient, err = rpc.DialHTTP("tcp", "127.0.0.1:29999")
	defer tempomatClient.Close()
	if err != nil {
		log.Fatalf("Failed to dial server: %s", err)
	}

	buf = image.NewRGBA(image.Rect(0, 0, scrWidth, scrHeight))
	labelBuf = image.NewRGBA(image.Rect(0, 0, scrWidth+250, scrHeight))

	fontCtx = freetype.NewContext()
	fontCtx.SetDPI(72)
	fontCtx.SetFont(fontFace)
	fontCtx.SetFontSize(10)
	fontCtx.SetClip(labelBuf.Bounds())
	fontCtx.SetDst(labelBuf)
	fontCtx.SetSrc(image.NewUniform(color.White))

	lastTick = time.Now()
	if err := ebiten.Run(update, scrWidth, scrHeight, 1.0, "Tempomat Show"); err != nil {
		log.Fatal(err)
	}
}

func isNice(l, a, b float64) bool {
	h, c, L := colorful.LabToHcl(l, a, b)
	return 150.0 < h && h < 250.0 && 0.2 < c && c < 0.8 && 0.0 < L && L < 0.8
}

func getData() *api.DumpList {
	slash32 := api.DumpList{}
	args := api.DumpArgs{
		BucketName: "Slash32",
	}
	err := tempomatClient.Call("TempomatAPI.Dump", &args, &slash32)
	if err != nil {
		log.Printf("Call error: %s", err)
		return nil
	}

	sort.Sort(api.TitleSortDumpList(slash32))
	return &slash32
}

func paint(slash32 *api.DumpList, fc *freetype.Context, buf, labelBuf *image.RGBA) {
	total := 0.0
	for _, e := range *slash32 {
		if time.Since(e.LastUsed) > maxAge {
			continue
		}

		// rps := 1.0/e.AvgSincePrev.Seconds()
		total += e.AvgCpuSecs

		if _, found := palMap[e.Title]; !found {
			palMap[e.Title] = palette[palPointer]

			palPointer++
			if palPointer >= len(palette) {
				// Rotate palette.
				palPointer = 0
			}
		}
	}

	curY := 0
	for _, e := range *slash32 {
		if time.Since(e.LastUsed) > maxAge {
			continue
		}

		length := int((e.AvgCpuSecs / total) * float64(scrHeight))
		nextY := curY + length
		for y := curY; y < nextY; y++ {
			buf.Set(scrWidth-1, y, palMap[e.Title])
		}

		if lastWritten, ok := labels[e.Title]; !ok || time.Since(lastWritten) > maxAge {
			pt := freetype.Pt(scrWidth, curY+10)
			fc.DrawString(e.Title, pt)
			labels[e.Title] = time.Now()
		}

		curY = nextY
	}
}

func moveLeft(buf *image.RGBA) {
	b := buf.Bounds()
	t := image.Pt(1, 0)
	draw.Draw(buf, b, buf, b.Min.Add(t), draw.Src)
}

func update(screen *ebiten.Image) error {
	if ebiten.IsRunningSlowly() {
		return nil
	}

	var data *api.DumpList
	if time.Since(lastTick) > tickPeriod {
		data = getData()
	}
	if data != nil {
		moveLeft(buf)
		moveLeft(labelBuf)
		paint(data, fontCtx, buf, labelBuf)
		img, err := ebiten.NewImageFromImage(buf, ebiten.FilterNearest)
		if err != nil {
			log.Fatalf("%s", err)
		}
		labelImg, err := ebiten.NewImageFromImage(labelBuf, ebiten.FilterNearest)
		if err != nil {
			log.Fatalf("%s", err)
		}
		mask := image.Rect(0, 0, scrWidth-1, scrHeight-1)
		opts := &ebiten.DrawImageOptions{
			SourceRect: &mask,
		}
		img.DrawImage(labelImg, opts)

		backImg = img
	}

	screen.DrawImage(backImg, nil)
	return nil
}
