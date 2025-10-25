package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"github.com/schollz/progressbar/v3"
	"github.com/tkrajina/gpxgo/gpx"
	_ "golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
)

const (
	tileCacheDir           = "tiles"
	tileSize               = 256
	slopeMaxEleChange      = 3.0
	slopeCalculationPoints = 5
)

// --- Structs ---

type MapStyle struct {
	Name    string
	URL     string
	Headers map[string]string
}

type Arguments struct {
	GpxFile          string
	OutputFile       string
	VideoWidth       int
	VideoHeight      int
	Bitrate          string
	Workers          int
	Framerate        float64
	MapStyle         string
	MapZoom          int
	WidgetSize       int
	PathWidth        float64
	PathColor        color.Color
	BorderColor      color.Color
	IndicatorColor   color.Color
	RenderFirstFrame bool
}

type Frame struct {
	Number int
	Data   []byte
}

type Point struct {
	Lat, Lon, Ele float64
	Timestamp     time.Time
}

type Tile struct {
	X, Y, Z int
}

type Track struct {
	Points         []Point
	SmoothedPoints []Point
	TotalDistance  float64
}

var mapStyles = map[string]MapStyle{
	"default":       {Name: "default", URL: "https://tile.openstreetmap.org/{z}/{x}/{y}.png"},
	"cyclosm":       {Name: "cyclosm", URL: "https://c.tile-cyclosm.openstreetmap.fr/cyclosm/{z}/{x}/{y}.png"},
	"toner":         {Name: "toner", URL: "https://tiles.stadiamaps.com/tiles/stamen_toner/{z}/{x}/{y}.png", Headers: map[string]string{"Referer": "https://mc.bbbike.org/"}},
	"clockwork":     {Name: "clockwork", URL: "https://maps.clockworkmicro.com/streets/v1/raster/{z}/{x}/{y}?x-api-key=2d33HqvhuU3z6lPsPOqQR6Zwl2LQ2pmo9NnWbboL"},
	"thunderforest": {Name: "thunderforest", URL: "https://c.tile.thunderforest.com/outdoors/{z}/{x}/{y}.png?apikey=6170aad10dfd42a38d4d8c709a536f38"},
	"positron":      {Name: "positron", URL: "https://d.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png"},
	"outdoor":       {Name: "outdoor", URL: "https://api.maptiler.com/maps/outdoor-v2/256/{z}/{x}/{y}.png?key=jsK0th32A1xWq2x6QeVu"},
}

// --- Argument Parsing ---

func parseArguments() *Arguments {
	args := &Arguments{}
	var pathColorStr, borderColorStr, indicatorColorStr string

	flag.StringVar(&args.GpxFile, "gpx", "example.gpx", "Path to the GPX file.")
	flag.StringVar(&args.OutputFile, "o", "output_go.mp4", "Output video file name.")
	flag.StringVar(&args.Bitrate, "bitrate", "5M", "Video bitrate (e.g., 5M).")
	flag.IntVar(&args.Workers, "workers", runtime.NumCPU(), "Number of parallel workers for frame generation.")
	flag.Float64Var(&args.Framerate, "framerate", 23.976, "Video framerate.")
	flag.StringVar(&args.MapStyle, "style", "default", "Map style (e.g., default, cyclosm, toner).")
	flag.IntVar(&args.MapZoom, "map-zoom", 16, "Map zoom level. Default 15 is approx 1km diameter for a 400px widget.")
	flag.IntVar(&args.WidgetSize, "widget-size", 600, "Map widget diameter in pixels.")
	pathWidth := flag.Float64("path-width", 4, "Width of the drawn path.")
	flag.StringVar(&pathColorStr, "path-color", "#FF0000", "Color of the drawn path (hex).")
	flag.StringVar(&borderColorStr, "border-color", "#ff9800", "Color of the map border (hex).")
	flag.StringVar(&indicatorColorStr, "indicator-color", "#FFFFFF", "Color of the text indicators (hex).")
	flag.BoolVar(&args.RenderFirstFrame, "render-first-frame", false, "Render only the first frame and save as first_frame.png.")

	fmt.Println(os.Args)
	flag.Parse()


	// Auto-calculate video size
	args.VideoWidth = args.WidgetSize + 40
	args.VideoHeight = args.WidgetSize + 200

	args.PathWidth = *pathWidth
	args.PathColor, _ = parseHexColor(pathColorStr)
	args.BorderColor, _ = parseHexColor(borderColorStr)
	args.IndicatorColor, _ = parseHexColor(indicatorColorStr)

	return args
}

func parseHexColor(s string) (color.Color, error) {
	var r, g, b uint8
	_, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	if err != nil {
		return color.Black, err
	}
	return color.RGBA{r, g, b, 255}, nil
}

// --- GPX Parsing & Processing ---

func parseGpx(filePath string) ([]Point, error) {
	gpxFile, err := gpx.ParseFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GPX file: %w", err)
	}

	var points []Point
	for _, track := range gpxFile.Tracks {
		for _, segment := range track.Segments {
			for _, p := range segment.Points {
				var ele float64
				if p.Elevation.NotNull() {
					ele = p.Elevation.Value()
				}
				points = append(points, Point{Lat: p.Latitude, Lon: p.Longitude, Ele: ele, Timestamp: p.Timestamp})
			}
		}
	}
	return points, nil
}

func preprocessGpxPoints(points []Point) []Point {
	if len(points) == 0 {
		return points
	}
	smoothed := make([]Point, len(points))
	copy(smoothed, points)

	for i := 1; i < len(smoothed); i++ {
		if math.Abs(smoothed[i].Ele-smoothed[i-1].Ele) > slopeMaxEleChange {
			smoothed[i].Ele = smoothed[i-1].Ele
		}
	}
	return smoothed
}

// --- Coordinate & Tile Math ---

func deg2num(lat, lon float64, zoom int) (float64, float64) {
	latRad := lat * math.Pi / 180
	n := math.Pow(2, float64(zoom))
	xtile := (lon + 180) / 360 * n
	ytile := (1 - math.Asinh(math.Tan(latRad))/math.Pi) / 2 * n
	return xtile, ytile
}

// --- Tile Downloading & Caching ---

var tileCache sync.Map // Concurrent map for caching tiles

func getTileImage(style string, z, x, y int) (image.Image, error) {
	styleInfo, ok := mapStyles[style]
	if !ok {
		panic(fmt.Sprintf("invalid map style: %s", style))
	}
	tilePath := filepath.Join(tileCacheDir, styleInfo.Name, strconv.Itoa(z), strconv.Itoa(x), fmt.Sprintf("%d.png", y))

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
		tileCache.Store(tilePath, img)
		return img, nil
	}

	// Download
	url := strings.Replace(styleInfo.URL, "{z}", strconv.Itoa(z), 1)
	url = strings.Replace(url, "{x}", strconv.Itoa(x), 1)
	url = strings.Replace(url, "{y}", strconv.Itoa(y), 1)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "GpsOverlayVideoGo/0.1")
	for k, v := range styleInfo.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to download tile %s: %v", url, err)
	}
	defer resp.Body.Close()

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}

	os.MkdirAll(filepath.Dir(tilePath), 0755)
	out, err := os.Create(tilePath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	png.Encode(out, img)

	tileCache.Store(tilePath, img)
	return img, nil
}

// --- Main Logic ---

func main() {
	args := parseArguments()

	points, err := parseGpx(args.GpxFile)
	if err != nil {
		log.Fatalf("Error parsing GPX: %v", err)
	}
	if len(points) < 2 {
		log.Fatal("Not enough points in GPX file.")
	}

	track := &Track{Points: points}
	track.SmoothedPoints = preprocessGpxPoints(track.Points)
	for i := 1; i < len(track.Points); i++ {
		track.TotalDistance += haversine(track.Points[i-1], track.Points[i])
	}

	font, err := truetype.Parse(goregular.TTF)
	if err != nil {
		log.Fatal(err)
	}

	if args.RenderFirstFrame {
		log.Println("Rendering first frame only...")
		img := renderFrame(0, 1, track, args, font)
		gg.SavePNG("first_frame.png", img)
		log.Println("Saved first_frame.png")
		return
	}

	// --- FFMPEG Setup ---
	ffmpegCmd := exec.Command("ffmpeg", "-y", "-f", "image2pipe", "-vcodec", "png", "-r", fmt.Sprintf("%f", args.Framerate), "-i", "-", "-c:v", "libx264", "-b:v", args.Bitrate, "-pix_fmt", "yuva420p", "-r", fmt.Sprintf("%f", args.Framerate), args.OutputFile)
	ffmpegIn, err := ffmpegCmd.StdinPipe()
	if err != nil {
		log.Fatalf("Failed to get ffmpeg stdin pipe: %v", err)
	}
	ffmpegCmd.Stderr = os.Stderr
	if err := ffmpegCmd.Start(); err != nil {
		log.Fatalf("Failed to start ffmpeg: %v", err)
	}

	// --- Prefetch Tiles ---
	prefetchTiles(track.Points, args)

	// --- Concurrency Setup ---
	var wg sync.WaitGroup
	frameChan := make(chan Frame, int(args.Framerate)*2)
	totalDuration := track.Points[len(track.Points)-1].Timestamp.Sub(track.Points[0].Timestamp)
	totalFrames := int(totalDuration.Seconds() * args.Framerate)

	// --- Encoder Goroutine ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		bar := progressbar.Default(int64(totalFrames), "Encoding")
		for i := 0; i < totalFrames; i++ {
			frame, ok := <-frameChan
			if !ok {
				break
			}
			_, err := ffmpegIn.Write(frame.Data)
			if err != nil {
				log.Printf("Error writing to ffmpeg: %v", err)
			}
			bar.Add(1)
		}
		ffmpegIn.Close()
	}()

	// --- Frame Generation ---
	generateFrames(frameChan, track, args, totalFrames, font)
	close(frameChan)

	wg.Wait()
	if err := ffmpegCmd.Wait(); err != nil {
		log.Fatalf("ffmpeg command failed: %v", err)
	}

	fmt.Printf("\nVideo saved to %s\n", args.OutputFile)
}

func prefetchTiles(points []Point, args *Arguments) {
	log.Println("Prefetching map tiles...")
	tileCoords := make(map[Tile]struct{})
	widgetRadiusPx := float64(args.WidgetSize) / 2.0

	for _, p := range points {
		worldPx, worldPy := deg2num(p.Lat, p.Lon, args.MapZoom)
		worldPx *= tileSize
		worldPy *= tileSize

		px_min := worldPx - widgetRadiusPx
		py_min := worldPy - widgetRadiusPx
		px_max := worldPx + widgetRadiusPx
		py_max := worldPy + widgetRadiusPx

		tx_min := math.Floor(px_min / tileSize)
		ty_min := math.Floor(py_min / tileSize)
		tx_max := math.Floor(px_max / tileSize)
		ty_max := math.Floor(py_max / tileSize)

		for x := int(tx_min); x <= int(tx_max); x++ {
			for y := int(ty_min); y <= int(ty_max); y++ {
				tileCoords[Tile{X: x, Y: y, Z: args.MapZoom}] = struct{}{}
			}
		}
	}

	bar := progressbar.Default(int64(len(tileCoords)), "Downloading Tiles")
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)

	for tile := range tileCoords {
		wg.Add(1)
		limit <- struct{}{}
		go func(t Tile) {
			defer wg.Done()
			getTileImage(args.MapStyle, t.Z, t.X, t.Y)
			bar.Add(1)
			<-limit
		}(tile)
	}
	wg.Wait()
}

func generateFrames(frameChan chan<- Frame, track *Track, args *Arguments, totalFrames int, font *truetype.Font) {
	var wg sync.WaitGroup
	tasks := make(chan int, totalFrames)
	for i := 0; i < totalFrames; i++ {
		tasks <- i
	}
	close(tasks)

	for i := 0; i < args.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pngBuffer := new(bytes.Buffer)

			for frameNum := range tasks {
				img := renderFrame(frameNum, totalFrames, track, args, font)
				
				pngBuffer.Reset()
				err := png.Encode(pngBuffer, img)
				if err != nil {
					log.Printf("Failed to encode frame %d: %v", frameNum, err)
					continue
				}

				frameData := make([]byte, pngBuffer.Len())
				copy(frameData, pngBuffer.Bytes())

				frameChan <- Frame{Number: frameNum, Data: frameData}
			}
		}()
	}
	wg.Wait()
}

func drawSpeedIcon(dc *gg.Context, x, y, size, lineWidth float64) {
	dc.Push()
	dc.Translate(x, y)
	dc.SetLineWidth(lineWidth)

	// Draw a semicircle from 165 to 375 degrees
	startAngle := gg.Radians(165)
	endAngle := gg.Radians(375)
	dc.DrawArc(0, 0, size/2, startAngle, endAngle)
	dc.Stroke()

	// Draw the needle
	needleAngle := gg.Radians(210) // Example angle
	dc.MoveTo(0, 0)
	dc.LineTo(math.Cos(needleAngle)*size/2.2, math.Sin(needleAngle)*size/2.2)
	dc.Stroke()
	dc.Pop()
}

func drawSlopeIcon(dc *gg.Context, x, y, size, lineWidth float64) {
	dc.Push()
	dc.Translate(x, y)
	dc.SetLineWidth(lineWidth)
	// Draw a 30-degree slope triangle
	angle := gg.Radians(30)
	legX := size
	legY := size * math.Tan(angle)
	dc.MoveTo(legX, legY/2)
	dc.LineTo(0, legY/2)
	dc.LineTo(legX, -legY/2)
	dc.Stroke()
	dc.Pop()
}

func renderFrame(frameNum, totalFrames int, track *Track, args *Arguments, font *truetype.Font) image.Image {
	startTime := track.Points[0].Timestamp
	timeOffset := float64(frameNum) / args.Framerate
	currentPoint := findPointForTime(timeOffset, startTime, track.SmoothedPoints)

	// --- Calculations ---
	var speed, slope, currentDistance float64
	pathSoFar := []Point{}
	for i := 0; i < len(track.Points) && track.Points[i].Timestamp.Before(currentPoint.Timestamp); i++ {
		if i > 0 {
			currentDistance += haversine(track.Points[i-1], track.Points[i])
		}
		pathSoFar = append(pathSoFar, track.Points[i])
	}
	pathSoFar = append(pathSoFar, currentPoint)

	if len(pathSoFar) > 1 {
		p1 := pathSoFar[len(pathSoFar)-2]
		p2 := pathSoFar[len(pathSoFar)-1]
		distDelta := haversine(p1, p2)
		timeDelta := p2.Timestamp.Sub(p1.Timestamp).Seconds()
		if timeDelta > 0 {
			speed = (distDelta * 3600) / timeDelta
		}
	}

	// Smoothed Slope
	if len(pathSoFar) > slopeCalculationPoints {
		p1 := pathSoFar[len(pathSoFar)-1-slopeCalculationPoints]
		p2 := pathSoFar[len(pathSoFar)-1]
		horizDist := haversine(p1, p2) * 1000
		eleDelta := p2.Ele - p1.Ele
		if horizDist > 0 {
			slope = (eleDelta / horizDist) * 100
		}
	}

	// --- Map Rendering ---
	widgetRadiusPx := float64(args.WidgetSize) / 2.0
	worldPx, worldPy := deg2num(currentPoint.Lat, currentPoint.Lon, args.MapZoom)
	worldPx *= tileSize
	worldPy *= tileSize

	px_min := worldPx - widgetRadiusPx
	py_min := worldPy - widgetRadiusPx
	px_max := worldPx + widgetRadiusPx
	py_max := worldPy + widgetRadiusPx

	tx_min := math.Floor(px_min / tileSize)
	ty_min := math.Floor(py_min / tileSize)
	tx_max := math.Floor(px_max / tileSize)
	ty_max := math.Floor(py_max / tileSize)

	mapWidth := (int(tx_max) - int(tx_min) + 1) * tileSize
	mapHeight := (int(ty_max) - int(ty_min) + 1) * tileSize
	mapImage := image.NewRGBA(image.Rect(0, 0, mapWidth, mapHeight))
	mapDC := gg.NewContextForRGBA(mapImage)

	for x := int(tx_min); x <= int(tx_max); x++ {
		for y := int(ty_min); y <= int(ty_max); y++ {
			tileImg, _ := getTileImage(args.MapStyle, args.MapZoom, x, y)
			if tileImg != nil {
				mapDC.DrawImage(tileImg, (x-int(tx_min))*tileSize, (y-int(ty_min))*tileSize)
			}
		}
	}

	centerPxOnMap := worldPx - (tx_min * tileSize)
	centerPyOnMap := worldPy - (ty_min * tileSize)

	// Path
	if len(pathSoFar) > 1 {
		mapDC.SetColor(args.PathColor)
		mapDC.SetLineWidth(args.PathWidth)
		for i := 1; i < len(pathSoFar); i++ {
			p1x, p1y := deg2num(pathSoFar[i-1].Lat, pathSoFar[i-1].Lon, args.MapZoom)
			p2x, p2y := deg2num(pathSoFar[i].Lat, pathSoFar[i].Lon, args.MapZoom)
			mapDC.DrawLine((p1x-tx_min)*tileSize, (p1y-ty_min)*tileSize, (p2x-tx_min)*tileSize, (p2y-ty_min)*tileSize)
			mapDC.Stroke()
		}
	}

	// Current position marker
	mapDC.SetColor(color.RGBA{0, 0, 255, 255})
	mapDC.DrawPoint(centerPxOnMap, centerPyOnMap, 8)
	mapDC.Fill()
	mapDC.SetColor(color.White)
	mapDC.SetLineWidth(2)
	mapDC.DrawPoint(centerPxOnMap, centerPyOnMap, 8)
	mapDC.Stroke()

	// Crop circular widget
	mask := gg.NewContext(args.WidgetSize, args.WidgetSize)
	mask.DrawCircle(widgetRadiusPx, widgetRadiusPx, widgetRadiusPx)
	mask.Clip()
	mask.DrawImage(mapDC.Image(), -int(centerPxOnMap-widgetRadiusPx), -int(centerPyOnMap-widgetRadiusPx))
	
	// --- Final Frame Composition ---
	frameDC := gg.NewContext(args.VideoWidth, args.VideoHeight)
	mapPosX := float64(20)
	mapPosY := float64(20)
	frameDC.DrawImage(mask.Image(), int(mapPosX), int(mapPosY))

	// 3D Border
	borderWidth := float64(args.WidgetSize) * 0.04
	// Shadow (bottom-right)
	frameDC.SetColor(color.RGBA{R: 0, G: 0, B: 0, A: 80})
	frameDC.SetLineWidth(borderWidth * 0.75)
	frameDC.DrawArc(mapPosX+widgetRadiusPx+borderWidth/3, mapPosY+widgetRadiusPx+borderWidth/3, widgetRadiusPx, gg.Radians(-45), gg.Radians(135))
	frameDC.Stroke()
	// Highlight (top-left)
	frameDC.SetColor(color.RGBA{R: 255, G: 255, B: 255, A: 80})
	frameDC.DrawArc(mapPosX+widgetRadiusPx+borderWidth/3, mapPosY+widgetRadiusPx+borderWidth/3, widgetRadiusPx, gg.Radians(135), gg.Radians(315))
	frameDC.Stroke()
	// Main Border
	frameDC.SetColor(args.BorderColor)
	frameDC.SetLineWidth(borderWidth)
	frameDC.DrawCircle(mapPosX+widgetRadiusPx, mapPosY+widgetRadiusPx, widgetRadiusPx)
	frameDC.Stroke()

	// --- Indicators ---

	// Proportional sizing
	widgetWidth := float64(args.WidgetSize)
	valueFontSize := widgetWidth / 8.0
	unitFontSize := valueFontSize / 2.0
	iconSize := widgetWidth / 9.0
	iconLineWidth := widgetWidth / 150.0
	
	valueFace := truetype.NewFace(font, &truetype.Options{Size: valueFontSize})
	unitFace := truetype.NewFace(font, &truetype.Options{Size: unitFontSize})

	row1Y := mapPosY + widgetWidth + valueFontSize*1.2
	
	frameDC.SetColor(args.IndicatorColor)

	// --- Speed Indicator (Left Third) ---
	speedBlockX := mapPosX
	speedBlockWidth := widgetWidth / 3.0
	
	speedIconX := speedBlockX + iconSize/2
	speedIconY := row1Y - 1.25 * valueFontSize
	drawSpeedIcon(frameDC, speedIconX, speedIconY, iconSize, iconLineWidth)

	speedValueText := fmt.Sprintf("%.1f", speed)
	speedUnitText := " km/h"
	
	frameDC.SetFontFace(valueFace)
	valueWidth, _ := frameDC.MeasureString(speedValueText)
	frameDC.SetFontFace(unitFace)
	unitWidth, _ := frameDC.MeasureString(speedUnitText)

	totalTextWidth := valueWidth + unitWidth
	startX := speedBlockX + speedBlockWidth - totalTextWidth

	frameDC.SetFontFace(valueFace)
	frameDC.DrawString(speedValueText, startX, row1Y)
	frameDC.SetFontFace(unitFace)
	frameDC.DrawString(speedUnitText, startX + valueWidth, row1Y)


	// --- Slope Indicator (Right Third) ---
	slopeBlockX := mapPosX + widgetWidth*2/3
	slopeBlockWidth := widgetWidth / 3.0

	slopeIconX := slopeBlockX + 2 * iconSize
	slopeIconY := row1Y - 1.25 * valueFontSize
	drawSlopeIcon(frameDC, slopeIconX, slopeIconY, iconSize, iconLineWidth)

	slopeValueText := fmt.Sprintf("%.1f", slope)
	slopeUnitText := " %"

	frameDC.SetFontFace(valueFace)
	valueWidth, _ = frameDC.MeasureString(slopeValueText)
	frameDC.SetFontFace(unitFace)
	unitWidth, _ = frameDC.MeasureString(slopeUnitText)

	totalTextWidth = valueWidth + unitWidth
	startX = slopeBlockX + slopeBlockWidth - totalTextWidth

	frameDC.SetFontFace(valueFace)
	frameDC.DrawString(slopeValueText, startX, row1Y)
	frameDC.SetFontFace(unitFace)
	frameDC.DrawString(slopeUnitText, startX + valueWidth, row1Y)


	// --- Row 2: Distance Bar ---
	row2Y := row1Y + unitFontSize*1.2
	barWidth := widgetWidth
	barHeight := 20.0
	progress := currentDistance / track.TotalDistance

	// Bar background
	frameDC.SetColor(color.RGBA{80, 80, 80, 255})
	frameDC.DrawRectangle(mapPosX, row2Y, barWidth, barHeight)
	frameDC.Fill()
	// Bar progress
	frameDC.SetColor(color.RGBA{100, 180, 255, 255})
	frameDC.DrawRectangle(mapPosX, row2Y, barWidth*progress, barHeight)
	frameDC.Fill()

	// Distance text
	distText := fmt.Sprintf("%.2f / %.2f km", currentDistance, track.TotalDistance)
	frameDC.SetColor(args.IndicatorColor)
	frameDC.SetFontFace(unitFace) // Using smaller font for distance text
	frameDC.DrawStringAnchored(distText, mapPosX+barWidth/2, row2Y+barHeight/2, 0.5, 0.5)

	return frameDC.Image()
}

func findPointForTime(offset float64, startTime time.Time, points []Point) Point {
	targetTime := startTime.Add(time.Duration(offset * float64(time.Second)))
	for i := 0; i < len(points)-1; i++ {
		p1, p2 := points[i], points[i+1]
		if (p1.Timestamp.Equal(targetTime) || p1.Timestamp.Before(targetTime)) && (p2.Timestamp.Equal(targetTime) || p2.Timestamp.After(targetTime)) {
			timeDiff := p2.Timestamp.Sub(p1.Timestamp).Seconds()
			if timeDiff == 0 {
				return p1
			}
			ratio := targetTime.Sub(p1.Timestamp).Seconds() / timeDiff
			return Point{
				Lat: p1.Lat + (p2.Lat-p1.Lat)*ratio,
				Lon: p1.Lon + (p2.Lon-p1.Lon)*ratio,
				Ele: p1.Ele + (p2.Ele-p1.Ele)*ratio,
				Timestamp: targetTime,
			}
		}
	}
	return points[len(points)-1]
}

func haversine(p1, p2 Point) float64 {
	const R = 6371 // Earth radius in kilometers
	lat1 := p1.Lat * math.Pi / 180
	lon1 := p1.Lon * math.Pi / 180
	lat2 := p2.Lat * math.Pi / 180
	lon2 := p2.Lon * math.Pi / 180

	dLat := lat2 - lat1
	dLon := lon2 - lon1

	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return R * c
}
