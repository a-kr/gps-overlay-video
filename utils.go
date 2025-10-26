package main

import (
	"flag"
	"fmt"
	"image/color"
	"math"
	"os"
	"runtime"
)

// --- Structs ---

type Arguments struct {
	GpxFile             string
	OutputFile          string
	VideoWidth          int
	VideoHeight         int
	Bitrate             string
	Workers             int
	Framerate           float64
	MapStyle            string
	MapZoom             int
	WidgetSize          int
	PathWidth           float64
	PathColor           color.Color
	BorderColor         color.Color
	IndicatorColor      color.Color
	RenderFirstFrame    bool
	Is2x                bool
	TileSize            int
	Debug               bool
	DynMapScale         bool
	TrackAdjustmentFile string
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
	flag.StringVar(&args.MapStyle, "style", "thunderforest", "Map style (e.g., default, cyclosm, toner).")
	flag.IntVar(&args.MapZoom, "map-zoom", 15, "Map zoom level. Default 15 is approx 1km diameter for a 400px widget.")
	flag.IntVar(&args.WidgetSize, "widget-size", 600, "Map widget diameter in pixels.")
	pathWidth := flag.Float64("path-width", 10, "Width of the drawn path.")
	flag.StringVar(&pathColorStr, "path-color", "#FF0000", "Color of the drawn path (hex).")
	flag.StringVar(&borderColorStr, "border-color", "#ff9800", "Color of the map border (hex).")
	flag.StringVar(&indicatorColorStr, "indicator-color", "#FFFFFF", "Color of the text indicators (hex).")
	flag.BoolVar(&args.RenderFirstFrame, "render-first-frame", false, "Render only the first frame and save as first_frame.png.")
	flag.BoolVar(&args.Is2x, "2x", true, "Use 2x tiles.")
	flag.BoolVar(&args.Debug, "debug", false, "Debug slope calculation.")
	flag.BoolVar(&args.DynMapScale, "dyn-map-scale", false, "Enable dynamic map scaling based on speed.")
	flag.StringVar(&args.TrackAdjustmentFile, "track-adjustment-file", "", "File with track adjustment specifications.")

	fmt.Println(os.Args)
	flag.Parse()

	// Auto-calculate video size
	args.VideoWidth = args.WidgetSize + 40
	args.VideoHeight = args.WidgetSize + 200

	args.PathWidth = *pathWidth
	args.PathColor, _ = parseHexColor(pathColorStr)
	args.BorderColor, _ = parseHexColor(borderColorStr)
	args.IndicatorColor, _ = parseHexColor(indicatorColorStr)

	if args.Is2x {
		args.TileSize = 512
	} else {
		args.TileSize = 256
	}

	return args
}

func parseHexColor(s string) (color.Color, error) {
	var r, g, b uint8
	_, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	if err != nil {
		return color.Black, err
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}, nil
}

func deg2num(lat, lon float64, zoom int) (float64, float64) {
	latRad := lat * math.Pi / 180
	n := math.Pow(2, float64(zoom))
	xtile := (lon + 180) / 360 * n
	ytile := (1 - math.Asinh(math.Tan(latRad))/math.Pi) / 2 * n
	return xtile, ytile
}
