package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tkrajina/gpxgo/gpx"
)

// --- Structs ---

type Point struct {
	Lat, Lon, Ele, Speed, Slope, Distance, SmoothedSlope, AvgSpeed, MapScale, ResidualMapScale, Bearing float64
	Timestamp      time.Time
	TileZoom       int
}

type Track struct {
	Points         []Point
	SmoothedPoints []Point
	TotalDistance  float64
	RenderFromIndex int
	RenderToIndex   int
}

type TrackAdjustmentSpec struct {
	PointSpec string
	Scale     float64
	Duration  *time.Duration
}

type ScaleChange struct {
	PointIndex         int
	TargetScale        float64
	TransitionDuration time.Duration
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
	var firstEle float64
	firstEleIdx := -1
	for i, p := range points {
		if p.Ele != 0 {
			firstEle = p.Ele
			firstEleIdx = i
			break
		}
	}

	if firstEleIdx != -1 {
		for i := 0; i < firstEleIdx; i++ {
			points[i].Ele = firstEle
		}
	}

	var lastEle float64
	if len(points) > 0 {
		lastEle = points[0].Ele
	}
	for i := range points {
		if points[i].Ele != 0 {
			lastEle = points[i].Ele
		} else {
			points[i].Ele = lastEle
		}
	}

	return points, nil
}

func parseTrackAdjustmentFile(filePath string) ([]TrackAdjustmentSpec, error) {
	if filePath == "" {
		return nil, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read track adjustment file: %w", err)
	}

	var specs []TrackAdjustmentSpec
	lines := strings.Split(string(content), `
`)

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid format on line %d: %s", i+1, line)
		}

		spec := TrackAdjustmentSpec{PointSpec: parts[0]}
		var scaleFound bool

		for _, part := range parts[1:] {
			if strings.HasPrefix(part, "scale=") {
				scaleStr := strings.TrimPrefix(part, "scale=")
				scale, err := strconv.ParseFloat(scaleStr, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid scale value on line %d: %s", i+1, line)
				}
				spec.Scale = scale
				scaleFound = true
			} else if strings.HasPrefix(part, "duration=") {
				durationStr := strings.TrimPrefix(part, "duration=")
				durationSec, err := strconv.ParseFloat(durationStr, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid duration value on line %d: %s", i+1, line)
				}
				duration := time.Duration(durationSec * float64(time.Second))
				spec.Duration = &duration
			} else {
				return nil, fmt.Errorf("unknown parameter on line %d: %s", i+1, part)
			}
		}

		if !scaleFound {
			return nil, fmt.Errorf("scale parameter not found on line %d: %s", i+1, line)
		}

		specs = append(specs, spec)
	}

	return specs, nil
}

func applyTrackAdjustments(points []Point, specs []TrackAdjustmentSpec) ([]float64, error) {
	scaleMultipliers := make([]float64, len(points))
	for i := range scaleMultipliers {
		scaleMultipliers[i] = 1.0
	}

	if len(specs) == 0 {
		return scaleMultipliers, nil
	}

	// --- Resolve specs to point indices ---
	scaleChanges := make([]ScaleChange, 0)
	lastDistance := 0.0
	lastTime := 0.0
	startTime := points[0].Timestamp

	for _, spec := range specs {
		var pointIndex int = -1
		transitionDuration := 20 * time.Second
		if spec.Duration != nil {
			transitionDuration = *spec.Duration
		}

		if spec.PointSpec == "0" {
			pointIndex = 0
		} else if strings.HasSuffix(spec.PointSpec, "km") {
			valStr := strings.TrimSuffix(strings.TrimPrefix(spec.PointSpec, "+"), "km")
			dist, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid distance spec: %s", spec.PointSpec)
			}

			if strings.HasPrefix(spec.PointSpec, "+") {
				dist += lastDistance
			}
			lastDistance = dist

			for i, p := range points {
				if p.Distance >= dist {
					pointIndex = i
					break
				}
			}
		} else if strings.HasSuffix(spec.PointSpec, "s") {
			valStr := strings.TrimSuffix(strings.TrimPrefix(spec.PointSpec, "+"), "s")
			timeOffset, err := strconv.ParseFloat(valStr, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid time spec: %s", spec.PointSpec)
			}

			if strings.HasPrefix(spec.PointSpec, "+") {
				timeOffset += lastTime
			}
			lastTime = timeOffset

			targetTime := startTime.Add(time.Duration(timeOffset * float64(time.Second)))
			for i, p := range points {
				if !p.Timestamp.Before(targetTime) {
					pointIndex = i
					break
				}
			}
		}

		if pointIndex != -1 {
			scaleChanges = append(scaleChanges, ScaleChange{PointIndex: pointIndex, TargetScale: spec.Scale, TransitionDuration: transitionDuration})
		} else {
			log.Printf("Warning: could not find point for spec '%s'", spec.PointSpec)
		}
	}

	// --- Apply scale changes to the multiplier slice ---
	currentScale := 1.0
	changeIdx := 0

	if len(scaleChanges) > 0 && scaleChanges[0].PointIndex == 0 {
		currentScale = scaleChanges[0].TargetScale
		changeIdx = 1
	}

	for i := range scaleMultipliers {
		scaleMultipliers[i] = currentScale
	}

	for i := changeIdx; i < len(scaleChanges); i++ {
		change := scaleChanges[i]
		prevChange := scaleChanges[i-1]
		prevScale := prevChange.TargetScale

		transitionDuration := change.TransitionDuration
		transitionStartIndex := change.PointIndex
		transitionStartTime := points[transitionStartIndex].Timestamp

		for j := transitionStartIndex; j < len(points); j++ {
			if i+1 < len(scaleChanges) && j >= scaleChanges[i+1].PointIndex {
				break
			}

			p := &points[j]
			timeSinceTransitionStart := p.Timestamp.Sub(transitionStartTime)

			if timeSinceTransitionStart < transitionDuration {
				progress := float64(timeSinceTransitionStart) / float64(transitionDuration)
				if progress < 0 {
					progress = 0
				} // Clamp progress
				logPrevScale := math.Log2(prevScale)
				logTargetScale := math.Log2(change.TargetScale)
				interpolatedLogScale := logPrevScale + progress*(logTargetScale-logPrevScale)
				interpolatedScale := math.Pow(2, interpolatedLogScale)
				scaleMultipliers[j] = interpolatedScale
			} else {
				scaleMultipliers[j] = change.TargetScale
			}
		}
	}

	return scaleMultipliers, nil
}

func preprocessGpxPoints(points []Point, args *Arguments) []Point {
	if len(points) < 2 {
		return points
	}
	smoothed := make([]Point, len(points))
	copy(smoothed, points)

	for i := 1; i < len(smoothed); i++ {
		if math.Abs(smoothed[i].Ele-smoothed[i-1].Ele) > slopeMaxEleChange {
			smoothed[i].Ele = smoothed[i-1].Ele
		}
	}

	for i := 1; i < len(smoothed); i++ {
		smoothed[i].Distance = smoothed[i-1].Distance + haversine(smoothed[i-1], smoothed[i])

		// Speed calculation (centered 5 points)
		windowStart := i - 2
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd := i + 2
		if windowEnd >= len(smoothed) {
			windowEnd = len(smoothed) - 1
		}

		var totalDist float64
		var totalTime float64
		for j := windowStart; j < windowEnd; j++ {
			totalDist += haversine(smoothed[j], smoothed[j+1])
			totalTime += smoothed[j+1].Timestamp.Sub(smoothed[j].Timestamp).Seconds()
		}
		if totalTime > 0 {
			smoothed[i].Speed = (totalDist * 3600) / totalTime
		} else if i > 0 {
			smoothed[i].Speed = smoothed[i-1].Speed
		} else {
			smoothed[i].Speed = 0
		}
	}

	// --- Moving Average Speed Calculation (30s window) ---
	if len(smoothed) > 0 {
		left, right := 0, 0
		var speedSum float64
		var speedCount int

		for i := range smoothed {
			// Window for point i
			windowStart := smoothed[i].Timestamp.Add(-avgSpeedWindow)
			windowEnd := smoothed[i].Timestamp.Add(avgSpeedWindow)

			// Expand window on the right
			for right < len(smoothed) && !smoothed[right].Timestamp.After(windowEnd) {
				speedSum += smoothed[right].Speed
				speedCount++
				right++
			}

			// Shrink window on the left
			for left < len(smoothed) && smoothed[left].Timestamp.Before(windowStart) {
				speedSum -= smoothed[left].Speed
				speedCount--
				left++
			}

			if speedCount > 0 {
				smoothed[i].AvgSpeed = speedSum / float64(speedCount)
			} else if i > 0 {
				smoothed[i].AvgSpeed = smoothed[i-1].AvgSpeed
			} else {
				smoothed[i].AvgSpeed = smoothed[i].Speed
			}
		}
	}

	// --- Dynamic Map Scale Calculation ---
	for i := range smoothed {
		speedMapScale := 1.0
		if args.DynMapScale {
			avgSpeed := smoothed[i].AvgSpeed
			if avgSpeed > dynMapScaleMinSpeedKmh {
				factor := (avgSpeed - dynMapScaleMinSpeedKmh) / (dynMapScaleMaxSpeedKmh - dynMapScaleMinSpeedKmh)
				if factor > 1.0 {
					factor = 1.0
				}
				speedMapScale = 1.0 + factor
			}
		}
		smoothed[i].MapScale = speedMapScale
	}

	for i := 0; i < len(smoothed)-1; i++ {
		smoothed[i].Bearing = bearing(smoothed[i], smoothed[i+1])
	}
	if len(smoothed) > 1 {
		smoothed[len(smoothed)-1].Bearing = smoothed[len(smoothed)-2].Bearing
	}
	// сглаживаем резкие прыжки bearing
	newBearings := make([]float64, len(smoothed))
	newBearings[0] = smoothed[0].Bearing
	for i := 1; i < len(smoothed)-1; i++ {
		b0 := smoothed[i-1].Bearing
		b1 := smoothed[i].Bearing
		if angleBetweenBearings(b0, b1) <= math.Pi/4 {
			newBearings[i] = b1
		} else { // too sharp a turn, keep the previous bearing until things calm down
			newBearings[i] = newBearings[i-1]
		}
	}
	for i := 1; i < len(smoothed)-1; i++ {
		smoothed[i].Bearing = newBearings[i]
	}
	// закончили сглаживать резкие прыжки bearing

	// --- Track Adjustments ---
	adjSpecs, err := parseTrackAdjustmentFile(args.TrackAdjustmentFile)
	if err != nil {
		log.Fatalf("Error processing track adjustment file: %v", err)
	}
	scaleMultipliers, err := applyTrackAdjustments(smoothed, adjSpecs)
	if err != nil {
		log.Fatalf("Error applying track adjustments: %v", err)
	}
	for i := range smoothed {
		smoothed[i].MapScale *= scaleMultipliers[i]
	}

	// --- Slope Calculation (centered 50m distance) ---
	for i := range smoothed {
		// Find the start point for our -25m slope calculation window
		p_start_idx := -1
		for j := i; j >= 0; j-- {
			if math.Abs(smoothed[i].Distance-smoothed[j].Distance)*1000 >= 25 {
				p_start_idx = j
				break
			}
		}

		// Find the end point for our +25m slope calculation window
		p_end_idx := -1
		for j := i; j < len(smoothed); j++ {
			if math.Abs(smoothed[j].Distance-smoothed[i].Distance)*1000 >= 25 {
				p_end_idx = j
				break
			}
		}

		if p_start_idx != -1 && p_end_idx != -1 {
			p_start := smoothed[p_start_idx]
			p_end := smoothed[p_end_idx]

			distance_delta := (p_end.Distance - p_start.Distance) * 1000 // meters
			elevation_delta := p_end.Ele - p_start.Ele

			if distance_delta > 1 { // Only calculate if distance is meaningful
				smoothed[i].Slope = (elevation_delta / distance_delta) * 100
			} else {
				smoothed[i].Slope = 0
			}
		} else if i > 0 {
			// If we can't find a full 50m window, carry over previous slope
			smoothed[i].Slope = smoothed[i-1].Slope
		} else {
			smoothed[i].Slope = 0
		}
	}

	// --- Smoothed Slope Calculation (5-second moving average) ---
	for i := 0; i < len(smoothed); i++ {
		start := i - 4
		if start < 0 {
			start = 0
		}

		var totalSlope float64
		count := 0
		for j := start; j <= i; j++ {
			totalSlope += smoothed[j].Slope
			count++
		}

		if count > 0 {
			smoothed[i].SmoothedSlope = totalSlope / float64(count)
		} else if i > 0 {
			smoothed[i].SmoothedSlope = smoothed[i-1].SmoothedSlope
		} else {
			smoothed[i].SmoothedSlope = 0
		}
	}

	// --- Pre-calculate Zoom and Scale ---
	for i := range smoothed {
		p := &smoothed[i]
		zoomOutLevels := 0.0
		if p.MapScale > 1.0 {
			zoomOutLevels = math.Floor(math.Log2(p.MapScale))
		}
		p.TileZoom = args.MapZoom - int(zoomOutLevels)
		if p.TileZoom < 0 {
			p.TileZoom = 0
		}
		p.ResidualMapScale = p.MapScale / math.Pow(2, zoomOutLevels)
	}

	return smoothed
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

func bearing(p1, p2 Point) float64 {
	lat1 := p1.Lat * math.Pi / 180
	lon1 := p1.Lon * math.Pi / 180
	lat2 := p2.Lat * math.Pi / 180
	lon2 := p2.Lon * math.Pi / 180

	dLon := lon2 - lon1

	y := math.Sin(dLon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLon)

	bearing := math.Atan2(y, x)

	return bearing // in radians
}

func angleBetweenBearings(bearing1, bearing2 float64) float64 {
	diff := bearing2 - bearing1
	diff = math.Mod(diff+math.Pi, 2*math.Pi) - math.Pi // Normalize to [-π, π]
	return math.Abs(diff)
}

func parseCutBoundary(boundary string, points []Point) int {
	if len(points) == 0 {
		return 0
	}
	if strings.HasSuffix(boundary, "s") {
		seconds, err := strconv.ParseFloat(strings.TrimSuffix(boundary, "s"), 64)
		if err != nil {
			return 0
		}
		startTime := points[0].Timestamp
		for i, p := range points {
			if p.Timestamp.Sub(startTime).Seconds() >= seconds {
				return i
			}
		}
		return len(points)
	} else if strings.HasSuffix(boundary, "km") {
		km, err := strconv.ParseFloat(strings.TrimSuffix(boundary, "km"), 64)
		if err != nil {
			return 0
		}
		for i, p := range points {
			if p.Distance >= km {
				return i
			}
		}
		return len(points)
	}
	return 0
}

func cutTrack(track *Track, from, to string) {
	track.RenderFromIndex = parseCutBoundary(from, track.SmoothedPoints)
	track.RenderToIndex = parseCutBoundary(to, track.SmoothedPoints)

	if track.RenderFromIndex >= track.RenderToIndex {
		track.RenderFromIndex = 0
		track.RenderToIndex = 0
	}
}
