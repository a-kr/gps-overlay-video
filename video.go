package main

import (
	"bytes"
	"fmt"
	"image/png"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/golang/freetype/truetype"
	"github.com/schollz/progressbar/v3"
)

// --- Structs ---

type Frame struct {
	Number int
	Data   []byte
}

// --- Video Pipeline ---

func generateFrames(frameChan chan<- Frame, track *Track, args *Arguments, totalFrames int, font *truetype.Font, segmentStartTime time.Time) {
	var wg sync.WaitGroup
	tasks := make(chan int, args.Workers*2)

	go func() {
		for i := 0; i < totalFrames; i++ {
			tasks <- i
		}
		close(tasks)
	}()

	for i := 0; i < args.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pngBuffer := new(bytes.Buffer)

			for frameNum := range tasks {
				img := renderFrame(frameNum, totalFrames, track, args, font, segmentStartTime)

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

func runVideoPipeline(track *Track, args *Arguments, font *truetype.Font) {
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

	// --- Concurrency Setup ---
	var wg sync.WaitGroup
	frameChan := make(chan Frame, int(args.Framerate)*2)

	if track.RenderToIndex == 0 {
		track.RenderToIndex = len(track.SmoothedPoints)
	}

	segmentDuration := track.SmoothedPoints[track.RenderToIndex-1].Timestamp.Sub(track.SmoothedPoints[track.RenderFromIndex].Timestamp)
	totalFrames := int(segmentDuration.Seconds() * args.Framerate)
	segmentStartTime := track.SmoothedPoints[track.RenderFromIndex].Timestamp

	// --- Encoder Goroutine (with reordering and timeout) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ffmpegIn.Close()

		bar := progressbar.Default(int64(totalFrames), "Encoding")
		frameBuffer := make(map[int][]byte)
		nextFrameToWrite := 0
		const frameWaitTimeout = 60 * time.Second
		timeout := time.NewTimer(frameWaitTimeout)

		for nextFrameToWrite < totalFrames {
			select {
			case frame, ok := <-frameChan:
				if !ok {
					log.Printf("Frame channel closed prematurely. Last written frame: %d", nextFrameToWrite-1)
					return
				}

				frameBuffer[frame.Number] = frame.Data
				if !timeout.Stop() {
					<-timeout.C
				}
				timeout.Reset(frameWaitTimeout)

				for {
					data, found := frameBuffer[nextFrameToWrite]
					if !found {
						break
					}

					_, err := ffmpegIn.Write(data)
					if err != nil {
						log.Printf("Error writing frame %d to ffmpeg: %v", nextFrameToWrite, err)
					}
					bar.Add(1)

					delete(frameBuffer, nextFrameToWrite)
					nextFrameToWrite++
				}

			case <-timeout.C:
				log.Fatalf("Timeout: Stuck waiting for frame %d for over %v. A worker may have hung.", nextFrameToWrite, frameWaitTimeout)
				return
			}
		}
	}()

	// --- Frame Generation ---
	generateFrames(frameChan, track, args, totalFrames, font, segmentStartTime)
	close(frameChan)

	wg.Wait()
	if err := ffmpegCmd.Wait(); err != nil {
		log.Fatalf("ffmpeg command failed: %v", err)
	}
}