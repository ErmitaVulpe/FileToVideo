# FileToVideo

Convert any file to a video and back

## Getting Started

### Dependencies

* Go compiler
* Ffmpeg

### Installing

```
git clone https://github.com/ErmitaVulpe/FileToVideo.git
cd FileToVideo
go build .
```

### Executing program

Encoding a file:
```
./FileToVideo -i input.file -o encoded.mp4
```

Decoding a video:
```
./FileToVideo -d -i encoded.mp4 -o decoded.file
```
