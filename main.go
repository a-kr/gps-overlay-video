package main

import (
	"fmt"
	"log"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font/gofont/goregular"
)

const (
	tileCacheDir           = "tiles"
	tileFetchConcurrency   = 8
	slopeMaxEleChange      = 3.0
	avgSpeedWindow         = 15 * time.Second
	dynMapScaleMinSpeedKmh = 17.0
	dynMapScaleMaxSpeedKmh = 26.0
)

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
	track.SmoothedPoints = preprocessGpxPoints(track.Points, args)
	for i := 1; i < len(track.Points); i++ {
		track.TotalDistance += haversine(track.Points[i-1], track.Points[i])
	}

	if args.Debug {
		for i := 1; i < len(track.SmoothedPoints); i++ {
			p := track.SmoothedPoints[i]
			fmt.Printf("Point %d: Speed: %.2f km/h, AvgSpeed: %.2f km/h, MapScale: %.2f, Slope: %.2f%%, SmoothedSlope: %.2f%%, TileZoom: %d, ResidualMapScale: %.2f\n", i, p.Speed, p.AvgSpeed, p.MapScale, p.Slope, p.SmoothedSlope, p.TileZoom, p.ResidualMapScale)
		}
		return
	}

	font, err := truetype.Parse(goregular.TTF)
	if err != nil {
		log.Fatal(err)
	}

	// --- Prefetch & Cache Tiles ---
	allTilesForTrack := getAllTilesForTrack(track, args)
	prefetchTiles(allTilesForTrack, args)

	adjSpecs, err := parseTrackAdjustmentFile(args.TrackAdjustmentFile)
	if err != nil {
		log.Fatalf("Error parsing track adjustment file: %v", err)
	}
	if adjSpecs != nil {
		uniqueScales := make(map[float64]struct{})
		for _, spec := range adjSpecs {
			uniqueScales[spec.Scale] = struct{}{}
		}
		cacheScaledTiles(uniqueScales, allTilesForTrack, args)
	}

	if args.RenderFirstFrame {
		log.Println("Rendering first frame only...")
		img := renderFrame(200, 1, track, args, font)
		gg.SavePNG("first_frame.png", img)
		log.Println("Saved first_frame.png")
		return
	}

	runVideoPipeline(track, args, font)

	fmt.Printf("\nVideo saved to %s\n", args.OutputFile)
}