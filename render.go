package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
)

func drawSpeedIcon(dc *gg.Context, x, y, size, lineWidth float64) {
	dc.Push()
	dc.Translate(x, y)
	dc.SetLineWidth(lineWidth)

	startAngle := gg.Radians(165)
	endAngle := gg.Radians(375)
	dc.DrawArc(0, 0, size/2, startAngle, endAngle)
	dc.Stroke()

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
	fiveSecondIntervalStartOffset := math.Floor(timeOffset/5.0) * 5.0
	slopeDisplayPoint := findPointForTime(fiveSecondIntervalStartOffset, startTime, track.SmoothedPoints)

	// --- Calculations ---
	pathSoFar := []Point{}
	for i := 0; i < len(track.Points) && track.Points[i].Timestamp.Before(currentPoint.Timestamp); i++ {
		pathSoFar = append(pathSoFar, track.Points[i])
	}
	pathSoFar = append(pathSoFar, currentPoint)

	speed := currentPoint.Speed
	slope := slopeDisplayPoint.SmoothedSlope
	currentDistance := currentPoint.Distance

	// --- Map Rendering Setup ---
	adjustedMapZoom := currentPoint.TileZoom
	residualMapScale := currentPoint.ResidualMapScale
	widgetRadiusPx := float64(args.WidgetSize) / 2.0

	var targetCachedResidualScale float64 = -1.0
	var scaleKey string
	for keyStr := range scaledTileCache {
		keyFloat, _ := strconv.ParseFloat(keyStr, 64)
		if math.Abs(residualMapScale-keyFloat) < 0.01 {
			targetCachedResidualScale = keyFloat
			scaleKey = keyStr
			break
		}
	}

	// --- Render Map Image ---
	var mapDC *gg.Context
	var centerPxOnMap, centerPyOnMap float64

	worldPx, worldPy := deg2num(currentPoint.Lat, currentPoint.Lon, adjustedMapZoom)
	worldPx *= float64(args.TileSize)
	worldPy *= float64(args.TileSize)

	if targetCachedResidualScale > 0 {
		// --- Cached Render Path ---
		scalingFactor := 1.0 / targetCachedResidualScale
		scaledTileSize := int(float64(args.TileSize) * scalingFactor)
		effectiveWidgetRadiusPx := widgetRadiusPx / scalingFactor

		px_min := worldPx - effectiveWidgetRadiusPx
		py_min := worldPy - effectiveWidgetRadiusPx
		px_max := worldPx + effectiveWidgetRadiusPx
		py_max := worldPy + effectiveWidgetRadiusPx

		tx_min := math.Floor(px_min / float64(args.TileSize))
		ty_min := math.Floor(py_min / float64(args.TileSize))
		tx_max := math.Floor(px_max / float64(args.TileSize))
		ty_max := math.Floor(py_max / float64(args.TileSize))

		mapWidth := (int(tx_max)-int(tx_min)+1) * scaledTileSize
		mapHeight := (int(ty_max)-int(ty_min)+1) * scaledTileSize
		mapImage := image.NewRGBA(image.Rect(0, 0, mapWidth, mapHeight))
		mapDC = gg.NewContextForRGBA(mapImage)

		for x := int(tx_min); x <= int(tx_max); x++ {
			for y := int(ty_min); y <= int(ty_max); y++ {
				tile := Tile{X: x, Y: y, Z: adjustedMapZoom}
				if scaledImg, ok := scaledTileCache[scaleKey][tile]; ok {
					mapDC.DrawImage(scaledImg, (x-int(tx_min))*scaledTileSize, (y-int(ty_min))*scaledTileSize)
				}
			}
		}

		centerPxOnMap = (worldPx - (tx_min * float64(args.TileSize))) * scalingFactor
		centerPyOnMap = (worldPy - (ty_min * float64(args.TileSize))) * scalingFactor

		// Path
		if len(pathSoFar) > 1 {
			mapDC.SetColor(args.PathColor)
			mapDC.SetLineWidth(args.PathWidth)
			for i := 1; i < len(pathSoFar); i++ {
				p1x, p1y := deg2num(pathSoFar[i-1].Lat, pathSoFar[i-1].Lon, adjustedMapZoom)
				p2x, p2y := deg2num(pathSoFar[i].Lat, pathSoFar[i].Lon, adjustedMapZoom)
				sp1x := (p1x*float64(args.TileSize) - tx_min*float64(args.TileSize)) * scalingFactor
				sp1y := (p1y*float64(args.TileSize) - ty_min*float64(args.TileSize)) * scalingFactor
				sp2x := (p2x*float64(args.TileSize) - tx_min*float64(args.TileSize)) * scalingFactor
				sp2y := (p2y*float64(args.TileSize) - ty_min*float64(args.TileSize)) * scalingFactor
				mapDC.DrawLine(sp1x, sp1y, sp2x, sp2y)
				mapDC.Stroke()
			}
		}
	} else {
		// --- Dynamic Scale Render Path ---
		effectiveWidgetRadiusPx := widgetRadiusPx * residualMapScale

		px_min := worldPx - effectiveWidgetRadiusPx
		py_min := worldPy - effectiveWidgetRadiusPx
		px_max := worldPx + effectiveWidgetRadiusPx
		py_max := worldPy + effectiveWidgetRadiusPx

		tx_min := math.Floor(px_min / float64(args.TileSize))
		ty_min := math.Floor(py_min / float64(args.TileSize))
		tx_max := math.Floor(px_max / float64(args.TileSize))
		ty_max := math.Floor(py_max / float64(args.TileSize))

		mapWidth := (int(tx_max) - int(tx_min) + 1) * args.TileSize
		mapHeight := (int(ty_max) - int(ty_min) + 1) * args.TileSize
		mapImage := image.NewRGBA(image.Rect(0, 0, mapWidth, mapHeight))
		mapDC = gg.NewContextForRGBA(mapImage)

		for x := int(tx_min); x <= int(tx_max); x++ {
			for y := int(ty_min); y <= int(ty_max); y++ {
				tileImg, err := getTileImage(args.MapStyle, adjustedMapZoom, x, y, args)
				if err != nil {
					log.Printf("could not get tile image: %v", err)
				}
				if tileImg != nil {
					mapDC.DrawImage(tileImg, (x-int(tx_min))*args.TileSize, (y-int(ty_min))*args.TileSize)
				}
			}
		}

		centerPxOnMap = worldPx - (tx_min * float64(args.TileSize))
		centerPyOnMap = worldPy - (ty_min * float64(args.TileSize))

		// Path
		if len(pathSoFar) > 1 {
			mapDC.SetColor(args.PathColor)
			mapDC.SetLineWidth(args.PathWidth)
			for i := 1; i < len(pathSoFar); i++ {
				p1x, p1y := deg2num(pathSoFar[i-1].Lat, pathSoFar[i-1].Lon, adjustedMapZoom)
				p2x, p2y := deg2num(pathSoFar[i].Lat, pathSoFar[i].Lon, adjustedMapZoom)
				mapDC.DrawLine((p1x-tx_min)*float64(args.TileSize), (p1y-ty_min)*float64(args.TileSize), (p2x-tx_min)*float64(args.TileSize), (p2y-ty_min)*float64(args.TileSize))
				mapDC.Stroke()
			}
		}
	}

	// --- Draw Marker & Compose ---
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

	if targetCachedResidualScale <= 0 && currentPoint.MapScale != 1.0 {
		// Apply dynamic scaling only if not using a cached version
		mask.Translate(widgetRadiusPx, widgetRadiusPx)
		if math.Abs(residualMapScale-1.0) > 0.01 {
			mask.Scale(1/residualMapScale, 1/residualMapScale)
		}
		mask.Translate(-widgetRadiusPx, -widgetRadiusPx)
	}

	mask.DrawImage(mapDC.Image(), -int(centerPxOnMap-widgetRadiusPx), -int(centerPyOnMap-widgetRadiusPx))

	// --- Final Frame Composition ---
	frameDC := gg.NewContext(args.VideoWidth, args.VideoHeight)
	mapPosX := float64(20)
	mapPosY := float64(20)
	frameDC.DrawImage(mask.Image(), int(mapPosX), int(mapPosY))

	// 3D Border
	borderWidth := float64(args.WidgetSize) * 0.04
	frameDC.SetColor(color.RGBA{R: 0, G: 0, B: 0, A: 80})
	frameDC.SetLineWidth(borderWidth * 0.75)
	frameDC.DrawArc(mapPosX+widgetRadiusPx+borderWidth/2, mapPosY+widgetRadiusPx+borderWidth/2, widgetRadiusPx, gg.Radians(-45), gg.Radians(135))
	frameDC.Stroke()
	frameDC.SetColor(color.RGBA{R: 255, G: 255, B: 255, A: 80})
	frameDC.DrawArc(mapPosX+widgetRadiusPx+borderWidth/2, mapPosY+widgetRadiusPx+borderWidth/2, widgetRadiusPx, gg.Radians(135), gg.Radians(315))
	frameDC.Stroke()
	frameDC.SetColor(args.BorderColor)
	frameDC.SetLineWidth(borderWidth)
	frameDC.DrawCircle(mapPosX+widgetRadiusPx, mapPosY+widgetRadiusPx, widgetRadiusPx)
	frameDC.Stroke()

	// --- Path and Marker (on top of map) ---
	widgetCenterX := mapPosX + widgetRadiusPx
	widgetCenterY := mapPosY + widgetRadiusPx

	// Set clip for path
	frameDC.Push()
	frameDC.DrawCircle(widgetCenterX, widgetCenterY, widgetRadiusPx)
	frameDC.Clip()

	if len(pathSoFar) > 1 {
		current_world_px, current_world_py := deg2num(currentPoint.Lat, currentPoint.Lon, adjustedMapZoom)
		frameDC.SetColor(args.PathColor)
		frameDC.SetLineWidth(args.PathWidth)
		for i := 1; i < len(pathSoFar); i++ {
			p1_world_px, p1_world_py := deg2num(pathSoFar[i-1].Lat, pathSoFar[i-1].Lon, adjustedMapZoom)
			p2_world_px, p2_world_py := deg2num(pathSoFar[i].Lat, pathSoFar[i].Lon, adjustedMapZoom)

			dx1 := (p1_world_px - current_world_px) * float64(args.TileSize)
			dy1 := (p1_world_py - current_world_py) * float64(args.TileSize)
			dx2 := (p2_world_px - current_world_px) * float64(args.TileSize)
			dy2 := (p2_world_py - current_world_py) * float64(args.TileSize)

			screen_dx1 := dx1 / residualMapScale
			screen_dy1 := dy1 / residualMapScale
			screen_dx2 := dx2 / residualMapScale
			screen_dy2 := dy2 / residualMapScale

			frameDC.DrawLine(widgetCenterX+screen_dx1, widgetCenterY+screen_dy1, widgetCenterX+screen_dx2, widgetCenterY+screen_dy2)
			frameDC.Stroke()
		}
	}
	frameDC.Pop() // Reset clip

	// Current position marker
	frameDC.SetColor(color.RGBA{0, 0, 255, 255})
	frameDC.DrawPoint(widgetCenterX, widgetCenterY, 8)
	frameDC.Fill()
	frameDC.SetColor(color.White)
	frameDC.SetLineWidth(2)
	frameDC.DrawPoint(widgetCenterX, widgetCenterY, 8)
	frameDC.Stroke()

	// --- Indicators ---
	widgetWidth := float64(args.WidgetSize)
	valueFontSize := widgetWidth / 8.0
	unitFontSize := valueFontSize / 2.0
	iconSize := widgetWidth / 9.0
	iconLineWidth := widgetWidth / 150.0

	valueFace := truetype.NewFace(font, &truetype.Options{Size: valueFontSize})
	unitFace := truetype.NewFace(font, &truetype.Options{Size: unitFontSize})

	row1Y := mapPosY + widgetWidth + valueFontSize*1.2

	frameDC.SetColor(args.IndicatorColor)

	// Speed Indicator
	speedBlockX := mapPosX
	speedBlockWidth := widgetWidth / 3.0
	speedIconX := speedBlockX + iconSize/2
	speedIconY := row1Y - 1.15*valueFontSize
	drawSpeedIcon(frameDC, speedIconX, speedIconY, iconSize, iconLineWidth)
	speedValueText := fmt.Sprintf("%.0f", math.Round(speed))
	speedUnitText := " km/h"
	frameDC.SetFontFace(valueFace)
	valueWidth, _ := frameDC.MeasureString(speedValueText)
	frameDC.SetFontFace(unitFace)
	unitWidth, _ := frameDC.MeasureString(speedUnitText)
	startX := speedBlockX + speedBlockWidth - (valueWidth + unitWidth)
	frameDC.SetFontFace(valueFace)
	frameDC.DrawString(speedValueText, startX, row1Y)
	frameDC.SetFontFace(unitFace)
	frameDC.DrawString(speedUnitText, startX+valueWidth, row1Y)

	// Slope Indicator
	slopeBlockX := mapPosX + widgetWidth*2/3
	slopeBlockWidth := widgetWidth / 3.0
	slopeIconX := slopeBlockX + iconSize/2
	slopeIconY := row1Y - 1.15*valueFontSize
	drawSlopeIcon(frameDC, slopeIconX, slopeIconY, iconSize, iconLineWidth)
	slopeValueText := fmt.Sprintf("%.1f", slope)
	slopeUnitText := " %"
	frameDC.SetFontFace(valueFace)
	valueWidth, _ = frameDC.MeasureString(slopeValueText)
	frameDC.SetFontFace(unitFace)
	unitWidth, _ = frameDC.MeasureString(slopeUnitText)
	startX = slopeBlockX + slopeBlockWidth - (valueWidth + unitWidth)
	frameDC.SetFontFace(valueFace)
	frameDC.DrawString(slopeValueText, startX, row1Y)
	frameDC.SetFontFace(unitFace)
	frameDC.DrawString(slopeUnitText, startX+valueWidth, row1Y)

	// Distance Bar
	row2Y := row1Y + unitFontSize*1.2
	barWidth := widgetWidth
	barHeight := 20.0
	progress := currentDistance / track.TotalDistance
	frameDC.SetColor(color.RGBA{80, 80, 80, 255})
	frameDC.DrawRectangle(mapPosX, row2Y, barWidth, barHeight)
	frameDC.Fill()
	frameDC.SetColor(color.RGBA{100, 180, 255, 255})
	frameDC.DrawRectangle(mapPosX, row2Y, barWidth*progress, barHeight)
	frameDC.Fill()
	distText := fmt.Sprintf("%.2f / %.2f km", currentDistance, track.TotalDistance)
	frameDC.SetColor(args.IndicatorColor)
	frameDC.SetFontFace(unitFace)
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
			derivedCalcRatio := ratio
			if timeDiff < 2.0 { // между точками малый интервал
				derivedCalcRatio = 0
			}
			p2ResidualMapScale := p2.ResidualMapScale
			if p1.TileZoom != p2.TileZoom {
				p2ResidualMapScale = p2.ResidualMapScale * math.Pow(2, float64(p1.TileZoom-p2.TileZoom))
			}
			return Point{
				Lat:              p1.Lat + (p2.Lat-p1.Lat)*ratio,
				Lon:              p1.Lon + (p2.Lon-p1.Lon)*ratio,
				Ele:              p1.Ele + (p2.Ele-p1.Ele)*ratio,
				Speed:            p1.Speed + (p2.Speed-p1.Speed)*derivedCalcRatio,
				AvgSpeed:         p1.AvgSpeed + (p2.AvgSpeed-p1.AvgSpeed)*derivedCalcRatio,
				Slope:            p1.Slope + (p2.Slope-p1.Slope)*derivedCalcRatio,
				SmoothedSlope:    p1.SmoothedSlope + (p2.SmoothedSlope-p1.SmoothedSlope)*derivedCalcRatio,
				Distance:         p1.Distance + (p2.Distance-p1.Distance)*derivedCalcRatio,
				MapScale:         p1.MapScale + (p2.MapScale-p1.MapScale)*ratio,
				Timestamp:        targetTime,
				TileZoom:         p1.TileZoom,
				ResidualMapScale: p1.ResidualMapScale + (p2ResidualMapScale-p1.ResidualMapScale)*ratio,
			}
		}
	}
	return points[len(points)-1]
}

