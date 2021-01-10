// What it does:
//
// This example opens a video capture device, then streams MJPEG from it.
// Once running point your browser to the hostname/port you passed in the
// command line (for example http://localhost:8080) and you should see
// the live video stream.
//
// How to run:
//
// detector config.toml
//
//		go get -u github.com/hybridgroup/mjpeg
// 		go run ./cmd/mjpeg-streamer/main.go 1 0.0.0.0:8080
//
// +build example

package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	_ "net/http/pprof"

	"github.com/hybridgroup/mjpeg"
	"github.com/pkg/profile"
	toml "github.com/pelletier/go-toml"
	"gocv.io/x/gocv"
)

type CameraCaptureInfo struct {
	Fps		float64
	Height		int
	Width		int
	Codec		string
}

type CameraConfigStruct struct {
	CameraId	int
	Name		string
	MinimumArea	float64
	FadeDelay	time.Duration
	InitDelay	time.Duration
	MaxDuration	time.Duration
	CaptureInfo	CameraCaptureInfo
	FocusArea	image.Rectangle
	VideoPath	string
	HttpName	string
	Debug		bool
	Record		bool
}

type GlobalConfigStruct struct {
	listen		string
	profile		bool
	DefaultWebInfo	CameraCaptureInfo
	webcams []CameraConfigStruct
}

func ParseResolution(resolution string, codec string) CameraCaptureInfo {
	// 1024x768 or 1024x768@15
	a := regexp.MustCompile("^(\\d+)x(\\d+)(@\\d+)?$")
	d := a.FindStringSubmatch(resolution)
	if len(codec) != 4 {
		codec = ""
	}
	frameRate := 15.0
	if d == nil {
		return CameraCaptureInfo{Codec: codec}
	} else {
		x, _ := strconv.Atoi(d[1])
		y, _ := strconv.Atoi(d[2])
		if len(d) == 4 {
			v, _ := strconv.Atoi(d[3][1:])
			frameRate = float64(v)
		}
		return CameraCaptureInfo{frameRate, y, x, codec}
	}
}

func ParseRectangle(text string) image.Rectangle {
	a := regexp.MustCompile("^(\\d+),(\\d+)-(\\d+),(\\d+$)")
	d := a.FindStringSubmatch(text)
	if d == nil {
		fmt.Printf("invalid rectang format, expected X1,Y1-X2-Y2")
		return image.Rectangle{}
	}
	x1, _ := strconv.Atoi(d[1])
	y1, _ := strconv.Atoi(d[1])
	x2, _ := strconv.Atoi(d[1])
	y2, _ := strconv.Atoi(d[1])

	var minX, maxX int
	var minY, maxY int

	if x1 < x2 {
		minX = x1
		maxX = x2
	} else {
		minX = x2
		maxX = x1
	}
	if y1 < y2 {
		minY = y1
		maxY = y2
	} else {
		minY = y2
		maxY = y1
	}

	topLeft := image.Pt(minX, minY)
	Max := image.Pt(maxX, maxY)
	return image.Rectangle{topLeft, Max}
}

func LoadConfig(configPath string) GlobalConfigStruct {
	cfgTree, err := toml.LoadFile(configPath)
	if err != nil {
		log.Fatalf("Error reading config %v: %v\n", configPath, err)
	}
	config := GlobalConfigStruct{}
	gTree := cfgTree.Get("global").(*toml.Tree)
	config.listen = gTree.GetDefault("listen", "0.0.0.0:8080").(string)
	config.profile = gTree.GetDefault("pprof", "off").(string) == "on"
	config.DefaultWebInfo = CameraCaptureInfo{Fps: 5.0}

	MinimumArea := gTree.GetDefault("minimum_area", 3000.0).(float64)
	FadeDelay := gTree.GetDefault("fade_delay", (3 * time.Second).Milliseconds()).(int64)
	InitDelay := gTree.GetDefault("init_delay", (5 * time.Second).Milliseconds()).(int64)
	MaxDuration := gTree.GetDefault("max_duration", (30 * time.Second).Milliseconds()).(int64)
	VideoPath := gTree.GetDefault("video_path", "/tmp/").(string)

	webcamsTree := cfgTree.GetArray("webcam").([]*toml.Tree)
	if webcamsTree == nil {
		log.Fatal("no cameras specified\n")
	}
	var webcams []CameraConfigStruct
	for _, camTree := range(webcamsTree) {
		var cameraId int = int(camTree.Get("id").(int64))
		resolution := camTree.GetDefault("resolution", "").(string)
		codec := camTree.GetDefault("camera_codec", "").(string)
		focus := ParseRectangle(camTree.GetDefault("focus_area", "").(string))
		camera := CameraConfigStruct {
			CameraId: cameraId,
			Name: camTree.GetDefault("name", fmt.Sprintf("camera%v", cameraId)).(string),
			MinimumArea: camTree.GetDefault("minimum_area", MinimumArea).(float64),
			FadeDelay: time.Duration(camTree.GetDefault("fade_delay", FadeDelay).(int64)) * time.Millisecond,
			InitDelay: time.Duration(camTree.GetDefault("init_delay", InitDelay).(int64)) * time.Millisecond,
			MaxDuration: time.Duration(camTree.GetDefault("max_duration", MaxDuration).(int64)) * time.Millisecond,
			VideoPath: camTree.GetDefault("video_path", VideoPath).(string),
			HttpName: camTree.Get("http_name").(string),
			Debug: camTree.GetDefault("debug", "off").(string) == "on",
			Record: camTree.GetDefault("record", "on").(string) == "on",
			CaptureInfo: ParseResolution(resolution, codec),
			FocusArea: focus,
		}
		webcams = append(webcams, camera)
	}
	config.webcams = webcams

	// TODO: check camera IDs
	for _, w := range(config.webcams) {
		fmt.Printf("[%v](%s): minimum_area=%v fade_delay=%v init_delay=%v path %v\n", w.CameraId, w.Name, w.MinimumArea, w.FadeDelay, w.InitDelay, w.VideoPath)
	}

	return config
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("How to run:\n\tdetect [config.toml]")
		return
	}

	config := LoadConfig(os.Args[1])
	if config.profile {
		defer profile.Start().Stop()
	}

	// start capturing
	for _, webcamConfig := range(config.webcams) {
		// create the mjpeg stream
		stream := mjpeg.NewStream()
		go cameraIO(webcamConfig, stream)
		http.Handle(fmt.Sprintf("/%s", webcamConfig.HttpName), stream)
	}

	fmt.Println("Capturing. Point your browser to " + config.listen)
	// start http server
	log.Fatal(http.ListenAndServe(config.listen, nil))
}

type motionDetector struct {
	imgSource	gocv.Mat
	imgDelta	gocv.Mat
	imgThresh	gocv.Mat
	mog2		gocv.BackgroundSubtractorMOG2
	minimumArea	float64
	fadeDelay	time.Duration
	initDelay	time.Duration
	lastDetected	time.Time
	resizeSize	image.Point
	resizeScale	int
	focusArea	image.Rectangle
	lastStatus	bool
	lastResult	bool
	lastRealResult	bool
	readOnly	bool
}

func getResizePoint(captureInfo CameraCaptureInfo) (int, image.Point) {
	width := captureInfo.Width
	height := captureInfo.Height
	mul := 1

	for width > 640 && height > 480 {
		width = width / 2
		height = height / 2
		mul *= 2
	}

	fmt.Printf("resize point: %vx%v, mul: 1/%v\n", width, height, mul)
	return mul, image.Pt(width, height)
}

func NewMotionDetector(config CameraConfigStruct, captureInfo CameraCaptureInfo) motionDetector {
	resizeScale, resizePoint := getResizePoint(captureInfo)

	topLeft := image.Pt(config.FocusArea.Min.X * resizePoint.X / 100,
			config.FocusArea.Min.Y * resizePoint.Y / 100)
	bottomRight := image.Pt(config.FocusArea.Min.X * resizePoint.X / 100,
			config.FocusArea.Max.Y * resizePoint.Y / 100)
	focus := image.Rectangle{topLeft, bottomRight}
	m := motionDetector {
		gocv.NewMat(),
		gocv.NewMat(),
		gocv.NewMat(),
		gocv.NewBackgroundSubtractorMOG2(),
		config.MinimumArea / float64(resizeScale),
		config.FadeDelay,
		config.InitDelay,
		time.Now().Add(-config.InitDelay),
		resizePoint,
		resizeScale,
		focus,
		false,
		false,
		false,
		!config.Debug,
	}
	return m
}

func (m *motionDetector) Close() {
	m.imgDelta.Close()
	m.imgThresh.Close()
	m.mog2.Close()
}

func scaleCountour(cnt []image.Point, scale float64) []image.Point {
	var ret []image.Point
	for _, dot := range(cnt) {
		newX := int(float64(dot.X) * scale)
		newY := int(float64(dot.Y) * scale)
		ret = append(ret, image.Pt(newX, newY))
	}
    return ret
}


func (m *motionDetector) _detectMotion(img *gocv.Mat) bool {

	// Resize
	gocv.Resize(*img, &m.imgSource, m.resizeSize, 0, 0, gocv.InterpolationNearestNeighbor)

	// Convert to grayscale
	gocv.CvtColor(m.imgSource, &m.imgSource, gocv.ColorRGBToGray)

	// first phase of cleaning up image, obtain foreground only
	m.mog2.Apply(m.imgSource, &m.imgDelta)

	// remaining cleanup of the image to use for finding contours.
	// first use threshold
	gocv.Threshold(m.imgDelta, &m.imgThresh, 25, 255, gocv.ThresholdBinary)

	// then dilate
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
	defer kernel.Close()
	gocv.Dilate(m.imgThresh, &m.imgThresh, kernel)

	result := false
	var a [][]image.Point
	// now find contours
	contours := gocv.FindContours(m.imgThresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	for i, c := range contours {
		area := gocv.ContourArea(c)
		if area < m.minimumArea {
			continue
		}

		result = true
		if !m.readOnly {
			a = append(a, scaleCountour(contours[i], float64(m.resizeScale)))
		}
	}

	for i, c := range(a) {
		statusColor := color.RGBA{0, 0, 255, 0}
		gocv.DrawContours(img, a, i, statusColor, 2)

		rect := gocv.BoundingRect(c)
		gocv.Rectangle(img, rect, color.RGBA{0, 255, 0, 0}, 2)
	}

	if !m.readOnly {
		points := []image.Point{m.focusArea.Min, m.focusArea.Max}
		s := scaleCountour(points, float64(m.resizeScale))
		rect := image.Rectangle{s[0], s[1]}
		gocv.Rectangle(img, rect, color.RGBA{0, 255, 255, 0}, 2)
	}

	return result
}

func (m *motionDetector) DetectMotion(img *gocv.Mat, frameCounter int) (bool, bool) {

	result := m._detectMotion(img)
	m.lastRealResult = result

	t := time.Now()
	if result {
		if m.lastStatus || (t.Sub(m.lastDetected) >= m.initDelay) {
			// Either continuation of motion or initDelay have passed
			m.lastDetected = t
			m.lastStatus = true
		} else {
			// need to wait for initDelay to pass
			result = false
		}
	} else {
		if t.Sub(m.lastDetected) < m.fadeDelay {
			// Fade delay haven't passed, return true
			result = true
		} else {
			m.lastStatus = false
		}
	}

	hasChanged := result != m.lastResult
	m.lastResult = result

	return result, hasChanged
}

func (m motionDetector) getFadeDelay() time.Duration {
	return time.Now().Sub(m.lastDetected)
}

func (m motionDetector) getText() (string, color.RGBA) {
	var text string
	var textColor color.RGBA

	if m.lastResult {
		fadeSeconds := time.Now().Sub(m.lastDetected).Seconds()
		if fadeSeconds < 1 {
			text = "Motion detected"
		} else {
			text = fmt.Sprintf("Motion detected [fade: %.1f]", fadeSeconds)
		}
		textColor = color.RGBA{255, 0, 0, 0}
	} else {
		if m.lastRealResult {
			text = "Steady [cooldown delay]"
		} else {
			text = "Steady"
		}
		textColor = color.RGBA{0, 255, 0, 0}
	}

	return text, textColor
}

func SetCaptureInfo(webcam *gocv.VideoCapture, info CameraCaptureInfo) {
	x := 4096
	y := 4096
	if info.Height > 0 {
		y = info.Height
		x = info.Width
	}
	fmt.Printf("Setting resolution to %vx%v\n", x, y)
	if len(info.Codec) == 4 {
		webcam.Set(gocv.VideoCaptureFOURCC, webcam.ToCodec(info.Codec))
	}
	webcam.Set(gocv.VideoCaptureFrameWidth, float64(x))
	webcam.Set(gocv.VideoCaptureFrameHeight, float64(y))
	webcam.Set(gocv.VideoCaptureFPS, info.Fps)
}

func GetCameraCaptureInfo(webcam *gocv.VideoCapture) *CameraCaptureInfo {
	result := CameraCaptureInfo {
		Fps: webcam.Get(gocv.VideoCaptureFPS),
		Width: int(webcam.Get(gocv.VideoCaptureFrameWidth)),
		Height: int(webcam.Get(gocv.VideoCaptureFrameHeight)),
		Codec: webcam.CodecString(),
	}
	return &result
}

type videoWriter struct {
	path	string
	writer	*gocv.VideoWriter
	frames	[]*gocv.Mat
}

type videoWriterIO struct {
	v		*videoWriter
	frames		[]*gocv.Mat
	closeWriter	bool
}

func NewVideoWriter(path string, captureInfo CameraCaptureInfo) (vv *videoWriter, err error) {
	t := time.Now()
	year, month, day := t.Date()
	hour, min, sec := t.Clock()
	extention := "avi"
	// codec := "XVID"
	codec := "MJPG"
	fname := fmt.Sprintf("%s/capture_%04d-%02d-%02d_%02d-%02d-%02d.%s", path, year, month, day, hour, min, sec, extention)
	fmt.Printf("capture file: %v\n", fname)
	// Decrease fps slightly
	fps := captureInfo.Fps - 1
	writer, err := gocv.VideoWriterFile(fname, codec, fps, captureInfo.Width, captureInfo.Height, true)
	if err != nil {
		fmt.Printf("error opening video writer device: %v\n", fname)
		return nil, err
	}

	v := videoWriter {fname, writer, make([]*gocv.Mat, 0)}
	return &v, nil
}

func (v *videoWriter) Write(img *gocv.Mat) {
	v.frames = append(v.frames, img)
	//v.writer.Write(*img)
}

func (v *videoWriter) GetIOBatch(closeWriter bool) *videoWriterIO {
	io := videoWriterIO{v, v.frames, closeWriter}
	v.frames = make([]*gocv.Mat, 0)

	return &io
}

func (v *videoWriter) PerformIO(frames []*gocv.Mat) {
	fmt.Printf("[async] dumping %v frames to disk\n", len(frames))
	for _, img := range(frames) {
		v.writer.Write(*img)
		img.Close()
	}
	fmt.Printf("[async] done\n")
}

func (v *videoWriter) Close() {
	v.writer.Close()
}

type videoHandler struct {
	v			*videoWriter
	motionDetected		bool
	hasChanged		bool
	record			bool
	path			string
	MaxDuration		time.Duration
	captureInfo		CameraCaptureInfo
	captureStart		time.Time
	frameCount		int
	totalFrameCount		int
	c			chan *videoWriterIO
}

func processVideoIO(c chan *videoWriterIO) {
	for io := range c {
		io.v.PerformIO(io.frames)
		if io.closeWriter {
			io.v.Close()
		}
	}
}

func NewVideoHandler(record bool, path string, maxDuration time.Duration, captureInfo CameraCaptureInfo) *videoHandler {
	c := make(chan *videoWriterIO, 20)
	go processVideoIO(c)

	h := videoHandler{
		nil,
		false,
		false,
		record,
		path,
		maxDuration,
		captureInfo,
		time.Now(),
		0,
		0,
		c,
	}
	return &h
}

func (h *videoHandler) ProcessFrame(img *gocv.Mat, motionDetected bool, hasChanged bool) bool {
	if (!h.record) {
		return false
	}

	t := time.Now()
	if motionDetected {
		if hasChanged {
			hour, min, sec := t.Clock()
			fmt.Printf("RECORD -  ON (%02d:%02d:%02d)\n", hour, min, sec)
			v, err := NewVideoWriter(h.path, h.captureInfo)
			if err != nil {
				fmt.Printf("error creating video writer\n")
				return false
			}
			h.v = v
			h.captureStart = t
			h.frameCount = 0
			h.totalFrameCount = 0
		}

		// Need to record the frame
		if t.Sub(h.captureStart) < h.MaxDuration {
			if h.v != nil {
				h.v.Write(img)
				h.frameCount += 1
				h.totalFrameCount += 1
				if h.frameCount > 100 {
					h.c <- h.v.GetIOBatch(false)
					h.frameCount = 0
				}
				return true
			}
		}
	} else {
		if hasChanged {
			hour, min, sec := t.Clock()
			dur := t.Sub(h.captureStart)
			if dur > h.MaxDuration {
				dur = h.MaxDuration
			}
			fps := float64(h.totalFrameCount) / dur.Seconds()
			fmt.Printf("RECORD - OFF (%02d:%02d:%02d) (dur: %.2fs, fps: %.2f)\n",
			    hour, min, sec, dur.Seconds(), fps)
			v := h.v
			h.v = nil
			h.c <- v.GetIOBatch(true)
		}
	}

	return false
}

func (h *videoHandler) Close() {
	close(h.c)
}

type perfMonitor struct {
	desiredFrames	float64
	minimumFrames	int
	lastSecond	int
	lastFrames	int
}

func NewPerfMonitor(captureInfo CameraCaptureInfo) perfMonitor {
	p := perfMonitor{
		desiredFrames:	captureInfo.Fps,
		minimumFrames:	int(captureInfo.Fps * 0.8),
	}
	return p
}

func (p *perfMonitor) PerfCheck(frameCounter int) {
	s := time.Now().Second()
	if s != p.lastSecond {
		if (p.lastFrames < p.minimumFrames) && (frameCounter > p.minimumFrames) {
			fmt.Printf("low fps: %v vs %v\n", p.lastFrames, p.desiredFrames)
		}
		//fmt.Printf(" FPS: %v\n", p.lastFrames)
		p.lastFrames = 0
		p.lastSecond = s
	} else {
		p.lastFrames += 1
	}
}

type streamUpdater struct {
	webRateDivisor		int
	stream			*mjpeg.Stream
	m			*motionDetector
	webImageSz		image.Point
	webImg			gocv.Mat
}

func NewStreamUpdater(stream *mjpeg.Stream, m *motionDetector, captureInfo CameraCaptureInfo) streamUpdater {
	s := streamUpdater {
		webRateDivisor:	int(captureInfo.Fps / 5.0),
		stream:		stream,
		m:		m,
		webImageSz:	image.Pt(captureInfo.Width / 2, captureInfo.Height / 2),
		webImg:		gocv.NewMat(),
	}
	return s
}

func (s *streamUpdater) UpdateWebStream(img *gocv.Mat, frameCounter int) {
	if (frameCounter % s.webRateDivisor) == 0 {
		text, textColor := s.m.getText()
		gocv.Resize(*img, &s.webImg, s.webImageSz, 0, 0, gocv.InterpolationNearestNeighbor)
		gocv.PutText(&s.webImg, text, image.Pt(10, 20), gocv.FontHersheyPlain, 1.2, textColor, 2)
		buf, _ := gocv.IMEncode(".jpg", s.webImg)
		s.stream.UpdateJPEG(buf)
	}
}

func (s *streamUpdater) Close() {
	s.webImg.Close()
}


func _cameraIO(config CameraConfigStruct, stream *mjpeg.Stream) {

	// open webcam
	webcam, err := gocv.OpenVideoCapture(config.CameraId)
	if err != nil {
		fmt.Printf("Error opening capture device: %v\n", config.CameraId)
		return
	}
	defer webcam.Close()
	SetCaptureInfo(webcam, config.CaptureInfo)

	captureInfo := GetCameraCaptureInfo(webcam)
	fmt.Printf("[%s] capturing %vx%v @ %v fps in %s (wanted %vx%v @ %v)\n",
		config.Name, captureInfo.Width, captureInfo.Height, captureInfo.Fps,
		captureInfo.Codec, config.CaptureInfo.Width, config.CaptureInfo.Height,
		config.CaptureInfo.Fps)

	m := NewMotionDetector(config, *captureInfo)
	defer m.Close()

	s := NewStreamUpdater(stream, &m, *captureInfo)
	defer s.Close()

	h := NewVideoHandler(config.Record, config.VideoPath, config.MaxDuration, *captureInfo)
	defer h.Close()

	p := NewPerfMonitor(*captureInfo)

	frameCounter := 0
	for {
		img := gocv.NewMat()
		if ok := webcam.Read(&img); !ok {
			fmt.Printf("Device closed: %v\n", config.CameraId)
			img.Close()
			return
		}
		if img.Empty() {
			img.Close()
			continue
		}
		frameCounter += 1

		p.PerfCheck(frameCounter)

		motionDetected, hasChanged := m.DetectMotion(&img, frameCounter)
		// Reads image & uses m
		s.UpdateWebStream(&img, frameCounter)

		if !h.ProcessFrame(&img, motionDetected, hasChanged) {
			img.Close()
		}
	}
}

func cameraIO(config CameraConfigStruct, stream *mjpeg.Stream) {
	for {
		_cameraIO(config, stream)
		time.Sleep(1 * time.Second)
	}
}
