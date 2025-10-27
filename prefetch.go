package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fogleman/gg"
	"github.com/schollz/progressbar/v3"
)

// --- Structs ---

type MapStyle struct {
	Name    string
	URL     string
	Headers map[string]string
}

type Tile struct {
	X, Y, Z int
}

var mapStyles = map[string]MapStyle{
	"default":       {Name: "default", URL: "https://tile.openstreetmap.org/{z}/{x}/{y}.png"},
	"cyclosm":       {Name: "cyclosm", URL: "https://c.tile-cyclosm.openstreetmap.fr/cyclosm/{z}/{x}/{y}.png"},
	"toner":         {Name: "toner", URL: "https://tiles.stadiamaps.com/tiles/stamen_toner/{z}/{x}/{y}.png", Headers: map[string]string{"Referer": "https://mc.bbbike.org/"}},
	"clockwork":     {Name: "clockwork", URL: "https://maps.clockworkmicro.com/streets/v1/raster/{z}/{x}/{y}?x-api-key=2d33HqvhuU3z6lPsPOqQR6Zwl2LQ2pmo9NnWbboL"},
	"thunderforest": {Name: "thunderforest", URL: "https://tile.thunderforest.com/outdoors/{z}/{x}/{y}.png?apikey=6170aad10dfd42a38d4d8c709a536f38"},
	"positron":      {Name: "positron", URL: "https://d.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png"},
	"outdoor":       {Name: "outdoor", URL: "https://api.maptiler.com/maps/outdoor-v2/256/{z}/{x}/{y}.png?key=jsK0th32A1xWq2x6QeVu"},
}

var (
	tileCache       sync.Map // Concurrent map for caching original tiles
	scaledTileCache = make(map[string]map[Tile]image.Image)
)

// --- Tile Downloading & Caching ---

func getTileImage(style string, z, x, y int, args *Arguments) (image.Image, error) {
	styleInfo, ok := mapStyles[style]
	if !ok {
		return nil, fmt.Errorf("invalid map style: %s", style)
	}

	tileName := fmt.Sprintf("%d.png", y)
	if args.Is2x {
		tileName = fmt.Sprintf("%d@2x.png", y)
	}
	tilePath := filepath.Join(tileCacheDir, styleInfo.Name, strconv.Itoa(z), strconv.Itoa(x), tileName)

	if img, ok := tileCache.Load(tilePath); ok {
		return img.(image.Image), nil
	}

	if _, err := os.Stat(tilePath); err == nil {
		file, err := os.Open(tilePath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		img, _, err := image.Decode(file)
		if err != nil {
			return nil, err
		}
		if args.Is2x && (img.Bounds().Dx() != 512 || img.Bounds().Dy() != 512) {
			return nil, fmt.Errorf("style %s does not support 2x: tile is %dx%d", style, img.Bounds().Dx(), img.Bounds().Dy())
		}
		if args.MapBrightness != 0 || args.MapContrast != 1 {
			img = adjustBrightnessContrast(img, args.MapBrightness, args.MapContrast)
		}
		tileCache.Store(tilePath, img)
		return img, nil
	}

	// Download
	url := strings.Replace(styleInfo.URL, "{z}", strconv.Itoa(z), 1)
	url = strings.Replace(url, "{x}", strconv.Itoa(x), 1)
	url = strings.Replace(url, "{y}", strconv.Itoa(y), 1)
	if args.Is2x {
		if strings.Contains(url, "outdoor-v2/256") {
			url = strings.Replace(url, "outdoor-v2/256", "outdoor-v2", 1)
		} else {
			url = strings.Replace(url, ".png", "@2x.png", 1)
		}
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "GpsOverlayVideoGo/0.1")
	for k, v := range styleInfo.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			log.Fatalf("Tile download timed out after 3 seconds for %s: %v", url, err)
		}
		return nil, fmt.Errorf("failed to download tile %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound && args.Is2x {
		return nil, fmt.Errorf("style %s does not support 2x (got 404 for tile: %s)", style, url)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download tile %s: status %d", url, resp.StatusCode)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}

	if args.Is2x && (img.Bounds().Dx() != 512 || img.Bounds().Dy() != 512) {
		return nil, fmt.Errorf("style %s does not support 2x: downloaded tile is %dx%d", style, img.Bounds().Dx(), img.Bounds().Dy())
	}

	os.MkdirAll(filepath.Dir(tilePath), 0755)
	out, err := os.Create(tilePath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	// Re-encode to PNG to save
	buf := new(bytes.Buffer)
	if err := png.Encode(buf, img); err != nil {
		return nil, err
	}
	out.Write(buf.Bytes())

	if args.MapBrightness != 0 || args.MapContrast != 1 {
		img = adjustBrightnessContrast(img, args.MapBrightness, args.MapContrast)
	}

	tileCache.Store(tilePath, img)
	return img, nil
}

func adjustBrightnessContrast(img image.Image, brightness, contrast float64) image.Image {
	bounds := img.Bounds()
	newImg := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()

			// Adjust brightness
			r_new := float64(r>>8) + brightness*255
			g_new := float64(g>>8) + brightness*255
			b_new := float64(b>>8) + brightness*255

			// Adjust contrast
			r_new = (r_new-128)*contrast + 128
			g_new = (g_new-128)*contrast + 128
			b_new = (b_new-128)*contrast + 128

			// Clamp values
			r_new = math.Max(0, math.Min(255, r_new))
			g_new = math.Max(0, math.Min(255, g_new))
			b_new = math.Max(0, math.Min(255, b_new))

			newImg.Set(x, y, color.RGBA{R: uint8(r_new), G: uint8(g_new), B: uint8(b_new), A: uint8(a >> 8)})
		}
	}
	return newImg
}

func getAllTilesForTrack(track *Track, args *Arguments) map[Tile]struct{} {
	tileCoords := make(map[Tile]struct{})

	for _, p := range track.SmoothedPoints {
		widgetRadiusPx := float64(args.WidgetSize) / 2.0

		adjustedMapZoom := p.TileZoom
		residualMapScale := p.ResidualMapScale
		effectiveWidgetRadiusPx := widgetRadiusPx * residualMapScale

		worldPx, worldPy := deg2num(p.Lat, p.Lon, adjustedMapZoom)
		worldPx *= float64(args.TileSize)
		worldPy *= float64(args.TileSize)

		px_min := worldPx - effectiveWidgetRadiusPx
		py_min := worldPy - effectiveWidgetRadiusPx
		px_max := worldPx + effectiveWidgetRadiusPx
		py_max := worldPy + effectiveWidgetRadiusPx

		tx_min := math.Floor(px_min / float64(args.TileSize))
		ty_min := math.Floor(py_min / float64(args.TileSize))
		tx_max := math.Floor(px_max / float64(args.TileSize))
		ty_max := math.Floor(py_max / float64(args.TileSize))

		for x := int(tx_min); x <= int(tx_max); x++ {
			for y := int(ty_min); y <= int(ty_max); y++ {
				tileCoords[Tile{X: x, Y: y, Z: adjustedMapZoom}] = struct{}{}
			}
		}
	}
	return tileCoords
}

func prefetchTiles(allTiles map[Tile]struct{}, args *Arguments) {
	log.Println("Prefetching map tiles...")
	bar := progressbar.Default(int64(len(allTiles)), "Downloading Tiles")
	var wg sync.WaitGroup
	limit := make(chan struct{}, tileFetchConcurrency)

	for tile := range allTiles {
		wg.Add(1)
		limit <- struct{}{}
		go func(t Tile) {
			defer wg.Done()
			getTileImage(args.MapStyle, t.Z, t.X, t.Y, args)
			bar.Add(1)
			<-limit
			time.Sleep(time.Second / 20) // Rate limit to 20 tiles per second
		}(tile)
	}
	wg.Wait()
}

func cacheScaledTiles(uniqueScales map[float64]struct{}, allTiles map[Tile]struct{}, args *Arguments) {
	if len(uniqueScales) == 0 {
		return
	}
	log.Println("Pre-scaling tiles for specified adjustments...")

	for scale := range uniqueScales {
		zoomOutLevels := 0.0
		if scale > 1.0 {
			zoomOutLevels = math.Floor(math.Log2(scale))
		}
		residualMapScale := scale / math.Pow(2, zoomOutLevels)
		scaleKey := fmt.Sprintf("%.4f", residualMapScale)

		if _, exists := scaledTileCache[scaleKey]; exists {
			continue
		}

		scalingFactor := 1.0 / residualMapScale
		if math.Abs(scalingFactor-1.0) < 0.01 {
			continue
		}

		log.Printf("Pre-scaling tiles for residual scale %.4f (%.2fx)...", residualMapScale, scalingFactor)
		scaledTileCache[scaleKey] = make(map[Tile]image.Image)
		bar := progressbar.Default(int64(len(allTiles)))

		for tile := range allTiles {
			bar.Add(1)
			originalImg, err := getTileImage(args.MapStyle, tile.Z, tile.X, tile.Y, args)
			if err != nil {
				log.Printf("could not get tile for scaling %v", err)
				continue
			}

			scaledWidth := int(float64(originalImg.Bounds().Dx()) * scalingFactor)
			scaledHeight := int(float64(originalImg.Bounds().Dy()) * scalingFactor)

			if scaledWidth == 0 || scaledHeight == 0 {
				continue
			}

			dc := gg.NewContext(scaledWidth, scaledHeight)
			dc.Scale(scalingFactor, scalingFactor)
			dc.DrawImage(originalImg, 0, 0)
			scaledImg := dc.Image()

			scaledTileCache[scaleKey][tile] = scaledImg
		}
	}
}
