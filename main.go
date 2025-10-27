package main

import (
	"fmt"
	"log"
	"math"
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
	track.RenderToIndex = len(track.SmoothedPoints)

	for i := 1; i < len(track.Points); i++ {
		track.TotalDistance += haversine(track.Points[i-1], track.Points[i])
	}

	cutTrack(track, args.From, args.To)

	if args.Debug {
		t0 := track.Points[0].Timestamp
		for i := track.RenderFromIndex; i < track.RenderToIndex; i++ {
			p := track.SmoothedPoints[i]
			ddist := 0.0
			if i > 0 {
				ddist = p.Distance - track.SmoothedPoints[i-1].Distance
			}
			fmt.Printf("Point %d: Time %v, Dist %.2f km, dDist %.4f km, Speed: %.2f km/h, AvgSpeed: %.2f km/h, MapScale: %.2f, Slope: %.2f%%, SmoothedSlope: %.2f%%, TileZoom: %d, ResidualMapScale: %.2f, Bearing: %.2f degrees\n", 
				i, p.Timestamp.Sub(t0), p.Distance, ddist, p.Speed, p.AvgSpeed, p.MapScale, p.Slope, p.SmoothedSlope, p.TileZoom, p.ResidualMapScale, p.Bearing * 180 / math.Pi)
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
		img := renderFrame(22000, 1, track, args, font, track.SmoothedPoints[0].Timestamp)
		gg.SavePNG("first_frame.png", img)
		log.Println("Saved first_frame.png")
		return
	}

	runVideoPipeline(track, args, font)

	fmt.Printf("\nVideo saved to %s\n", args.OutputFile)
}
