package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

const (
	dotSize                = 8
	frameWidth             = 1920
	frameHeight            = 1080
	rawBytesPerFrame       = frameWidth * frameHeight * 3 // 3 bytes per pixel
	processedBytesPerFrame = frameWidth / dotSize * frameHeight / dotSize * 3 / 8
)

type frameData struct {
	frameID int
	value   []byte
}

// --- Encode

func encode(srcFile, destFile string, threads int) {
	if frameWidth%dotSize != 0 || frameHeight%dotSize != 0 {
		panic("dotSize must be divisible both by 1920 and 1080")
	}
	width := int(frameWidth / dotSize)
	height := int(frameHeight / dotSize)

	start := time.Now()

	// Read the file bytes
	bytes, err := os.ReadFile(srcFile)
	if err != nil {
		panic(fmt.Sprintf("Error reading file: %s", err))
	}

	// Add length bytes
	bytesLength := make([]byte, 8)
	binary.BigEndian.PutUint64(bytesLength, uint64(len(bytes)))
	bytes = append(bytesLength, bytes...)

	rawFrames := [][]byte{}
	length := len(bytes)
	for i := 0; i < length; i += processedBytesPerFrame {
		end := i + processedBytesPerFrame
		if end > length {
			end = length
		}
		rawFrames = append(rawFrames, bytes[i:end])
	}

	ffmpegInstance := func(framesChanIn <-chan frameData, wg *sync.WaitGroup) {
		start = time.Now()
		defer wg.Done()

		// Start FFmpeg command and get its stdin pipe
		cmd := exec.Command("ffmpeg",
			"-y",             // Overwrite output file if it exists
			"-f", "rawvideo", // Input format as raw video
			"-pix_fmt", "rgba", // Pixel format as RGBA
			"-s", fmt.Sprintf("%dx%d", frameWidth, frameHeight), // Video size
			"-framerate", "60", // Frame rate
			"-i", "-", // Read input from pipe
			"-c:v", "h264_nvenc", // Input codec for GPU acceleration
			"-b:v", "30M", // Set the bitrate to 5 Mbps (adjust as needed)
			"-r", "60",
			"-x264opts", "keyint=300",
			"-g", "300",
			"-an",             // Disable audio processing
			"-preset", "fast", // Fast encoding profile
			destFile, // Output file path
		)

		// Open ffmpeg input
		stdin, err := cmd.StdinPipe()
		if err != nil {
			panic(err)
		}

		// Start the FFmpeg command
		err = cmd.Start()
		if err != nil {
			panic(err)
		}

		elapsed := time.Since(start)
		fmt.Printf("Opened ffmpeg in: %s\n", elapsed)

		buffer := map[int][]byte{}
		keys := []int{}
		keysLen := 0
		wantedID := 0
		frameID := 0

		for frame := range framesChanIn {
			if frame.frameID == wantedID {
				stdin.Write(frame.value)
				wantedID++

				if keysLen == 0 {
					continue
				}

				for keys[0] == wantedID {
					stdin.Write(buffer[wantedID])
					delete(buffer, wantedID)
					keys = keys[1:]
					keysLen--
					wantedID++
					if keysLen == 0 {
						break
					}
				}
			} else {
				frameID = frame.frameID
				buffer[frameID] = frame.value

				// "You can use a binary search algorithm to find the appropriate position for insertion." - ChatGPT
				index := sort.Search(len(keys), func(i int) bool {
					return keys[i] >= frameID
				})
				keys = append(keys, 0)             // Append a temporary element
				copy(keys[index+1:], keys[index:]) // Shift elements to the right
				keys[index] = frameID              // Insert the new number

				keysLen++
			}
		}

		// Close the stdin once all the data is written
		err = stdin.Close()
		if err != nil {
			panic(fmt.Sprintf("Error closing stdin: %s", err))
		}

		// Wait for the command to finish
		err = cmd.Wait()
		if err != nil {
			panic(fmt.Sprintf("Error waiting for command to finish: %s", err))

		}
	}

	serializer := func(framesChanIn <-chan frameData, frameProxyChan chan<- frameData, wg *sync.WaitGroup) {
		defer wg.Done()

		for iddFrame := range framesChanIn {
			frame := iddFrame.value
			pixelData := make([]byte, width*height*4*dotSize*dotSize)
			rowIterator := 0
			columnIterator := 0
			pixelCoords := 0
			pixel := make([]byte, 3)
			frameLen := len(frame)
			currByte := 0
			bitInByte := 7 // 0 is right most bit and i want to read from left to right
			for i := 0; i < len(pixelData); i += 4 {
				// Reset pixel
				pixel[0] = byte(0)
				pixel[1] = byte(0)
				pixel[2] = byte(0)

				// Iterate over RGB channels
				for j := 0; j < 3; j++ {
					if (frame[currByte] & (1 << bitInByte)) != 0 {
						pixel[j] = 0xff
					} else {
						pixel[j] = 0x00
					}

					if bitInByte == 0 { // Check if byte is finished
						currByte++
						if currByte == frameLen { // Check if it was the last byte
							i = 2147483647 // Gracefull outer break
							break
						}
						bitInByte = 7
					} else {
						bitInByte-- // Next bit
					}
				}

				// Map pixel to big pixel
				for row := rowIterator * dotSize; row < rowIterator*dotSize+dotSize; row++ {
					for column := columnIterator * dotSize; column < columnIterator*dotSize+dotSize; column++ {
						pixelCoords = column*7680 + row*4 // 7680 = 1920 * 4 channels
						copy(pixelData[pixelCoords:pixelCoords+3], pixel)
					}
				}
				rowIterator++
				if rowIterator == width {
					rowIterator = 0
					columnIterator++
				}
			}
			iddFrame.value = pixelData
			frameProxyChan <- iddFrame
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Read data in: %s\n", elapsed)
	start = time.Now()

	// Initialize ffmpegInstance group
	var ffmpegWaitGroup sync.WaitGroup
	ffmpegWaitGroup.Add(1)
	ffmpegInput := make(chan frameData)
	go ffmpegInstance(ffmpegInput, &ffmpegWaitGroup)

	// Initialize serializer group
	var serializerWaitGroup sync.WaitGroup
	rawFramesChan := make(chan frameData)
	for w := 1; w <= threads; w++ {
		serializerWaitGroup.Add(1)
		go serializer(rawFramesChan, ffmpegInput, &serializerWaitGroup)
	}

	for id, frame := range rawFrames {
		rawFramesChan <- frameData{frameID: id, value: frame}
	}

	close(rawFramesChan)
	serializerWaitGroup.Wait()

	elapsed = time.Since(start)
	fmt.Printf("frames digested in: %s\n", elapsed)

	close(ffmpegInput)
	ffmpegWaitGroup.Wait()

	fmt.Println("Video exported successfully")
}

// --- Decode

func decode(srcFile, destFile string, threads int) {
	// Ffmpeg instance runner goroutine
	var ffmpegWaitGroup sync.WaitGroup
	ffmpegWaitGroup.Add(1)
	ffmpegOutputChan := make(chan frameData)
	go func(ffmpegOutputChan chan<- frameData, wg *sync.WaitGroup) {
		cmd := exec.Command("ffmpeg",
			"-i", srcFile,
			"-vf", "format=rgb24",
			"-f", "rawvideo",
			"-preset", "fast",
			"-b:v", "100M",
			"-an",
			"-",
		)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Printf("Error creating stdout pipe: %s\n", err)
			return
		}

		if err := cmd.Start(); err != nil {
			fmt.Printf("Error starting command: %s\n", err)
			return
		}

		buffer := make([]byte, rawBytesPerFrame)
		frameCount := 0
		bytesRead := 0

		for {
			n, err := stdout.Read(buffer[bytesRead:])
			if err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					panic(fmt.Sprintf("Error reading from command output: %s\n", err))
				}
				break
			}

			bytesRead += n

			// Check if a full frame has been read
			if bytesRead == rawBytesPerFrame {
				// Create a new byte slice with the correct size for the frame
				frameDataBuffer := make([]byte, rawBytesPerFrame)
				copy(frameDataBuffer, buffer)

				ffmpegOutputChan <- frameData{frameID: frameCount, value: frameDataBuffer}
				frameCount++

				bytesRead = 0 // Reset bytesRead for the next frame
			}
		}

		// Wait for ffmpeg command to complete
		err = cmd.Wait()
		if err != nil {
			panic(fmt.Sprintf("Failed to wait for ffmpeg command: %s", err))
		}

		wg.Done()
	}(ffmpegOutputChan, &ffmpegWaitGroup)

	// Frame processing goroutines
	var frameDigesterWaitGroup sync.WaitGroup
	frameDigesterWaitGroup.Add(threads)
	digestedFramesChan := make(chan frameData)
	for i := 0; i < threads; i++ {
		go func(ffmpegOutputChan <-chan frameData, digestedFramesChan chan<- frameData, wg *sync.WaitGroup) {
			for frame := range ffmpegOutputChan {
				bytes := frame.value
				processedBytes := make([]byte, processedBytesPerFrame)
				byteIterator := 0
				pixelCoords := 0
				currByte := 0
				bitInByte := 7 // 0 is right most bit and i want to write from left to right
				for line := 3; line < 1080; line += 8 {
					for pixel := 9; pixel < 5760; pixel += 24 { // 5760 = 1920 * 3bytes
						pixelCoords = line*5760 + pixel
						for _, bit := range bytes[pixelCoords : pixelCoords+3] {
							if (bit & 0x80) != 0 {
								processedBytes[currByte] |= 1 << bitInByte
							}
							bitInByte--
							if bitInByte == -1 {
								currByte++
								bitInByte = 7
							}
						}
						byteIterator += 3
					}
				}

				frame.value = processedBytes
				digestedFramesChan <- frame
			}

			wg.Done()
		}(ffmpegOutputChan, digestedFramesChan, &frameDigesterWaitGroup)
	}

	// Writer goroutine
	var writerWaitGroup sync.WaitGroup
	writerWaitGroup.Add(1)
	go func(digestedFramesChan <-chan frameData, wg *sync.WaitGroup) {
		file, err := os.Create(destFile)
		if err != nil {
			panic(err)
		}

		// lastFrameOffset := (fileLength - 12142) % 12150
		var lastFrameOffset int64
		var lengthInt int64
		buffer := map[int][]byte{}
		keys := []int{}
		keysLen := 0
		wantedID := 1
		frameID := 0
		nextWriteByte := int64(processedBytesPerFrame - 8)
		// Address the first frame and truncate the file
		for frame := range digestedFramesChan {
			// Check if the recived frame is not the first one and if so, add to the buffer
			frameID = frame.frameID
			if frameID != 0 {
				buffer[frameID] = frame.value
				index := sort.Search(len(keys), func(i int) bool {
					return keys[i] >= frameID
				})
				keys = append(keys, 0)             // Append a temporary element
				copy(keys[index+1:], keys[index:]) // Shift elements to the right
				keys[index] = frameID              // Insert the new number
				keysLen++

				continue
			}

			frameValue := frame.value
			lengthBytes := frameValue[0:8]
			lengthInt = int64(binary.BigEndian.Uint64(lengthBytes))
			lastFrameOffset = (lengthInt - 12142) % 12150

			if err := file.Truncate(lengthInt); err != nil {
				panic(err)
			}

			if lengthInt < processedBytesPerFrame-8 {
				file.WriteAt(frameValue[8:lengthInt], 0)
			} else {
				file.WriteAt(frameValue[8:], 0)
			}

			break
		}

		for frame := range digestedFramesChan {
			frameID = frame.frameID
			if frameID == wantedID {
				if frameID == int(math.Ceil(float64(lengthInt+8)/float64(processedBytesPerFrame)))-1 {
					frame.value = frame.value[:lastFrameOffset]
				}
				file.WriteAt(frame.value, nextWriteByte)
				nextWriteByte += processedBytesPerFrame
				wantedID++

				if keysLen == 0 {
					continue
				}

				for keys[0] == wantedID {
					file.WriteAt(buffer[wantedID], nextWriteByte)
					delete(buffer, wantedID)
					keys = keys[1:]
					keysLen--
					wantedID++
					nextWriteByte += processedBytesPerFrame
					if keysLen == 0 {
						break
					}
				}
			} else {
				buffer[frameID] = frame.value
				index := sort.Search(len(keys), func(i int) bool {
					return keys[i] >= frameID
				})
				keys = append(keys, 0)             // Append a temporary element
				copy(keys[index+1:], keys[index:]) // Shift elements to the right
				keys[index] = frameID              // Insert the new number

				keysLen++
			}
		}

		file.Close()
		wg.Done()
	}(digestedFramesChan, &writerWaitGroup)

	// Wait for each group to finish
	ffmpegWaitGroup.Wait()
	close(ffmpegOutputChan)
	frameDigesterWaitGroup.Wait()
	close(digestedFramesChan)
	writerWaitGroup.Wait()

	fmt.Println("Video decoded successfully")
}
