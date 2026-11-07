// Package detect runs YOLOX object detection through ONNX Runtime.
package detect

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// Detection is one detected object, in original image coordinates.
type Detection struct {
	Class int
	Label string
	Score float32
	Box   image.Rectangle
}

// Options for the YOLOX detector.
type Options struct {
	// Legacy enables the old YOLOX preprocessing (RGB, /255, mean/std).
	// Models from recent YOLOX releases use raw BGR 0-255 (Legacy=false).
	Legacy bool
	// Threads limits CPU threads used by ONNX Runtime. 0 = ONNX Runtime default.
	Threads int
	// NMSThreshold is the IoU threshold for non-maximum suppression. 0 = 0.45.
	NMSThreshold float32
}

var (
	envOnce sync.Once
	envErr  error
)

// Init loads the ONNX Runtime shared library. Call once before NewYOLOX.
func Init(libPath string) error {
	envOnce.Do(func() {
		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}
		if err := ort.InitializeEnvironment(); err != nil {
			envErr = fmt.Errorf("init onnxruntime (%s): %w", libPath, err)
		}
	})
	return envErr
}

// Shutdown releases the ONNX Runtime environment.
func Shutdown() {
	if ort.IsInitialized() {
		_ = ort.DestroyEnvironment()
	}
}

// YOLOX is a loaded model. Detect is safe for concurrent use (calls are serialized).
type YOLOX struct {
	mu      sync.Mutex
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	output  *ort.Tensor[float32]
	inW     int
	inH     int
	classes int
	opts    Options
}

// NewYOLOX loads an exported YOLOX ONNX model (e.g. yolox_nano.onnx).
func NewYOLOX(modelPath string, opts Options) (*YOLOX, error) {
	if !ort.IsInitialized() {
		return nil, errors.New("onnxruntime not initialized, call detect.Init first")
	}
	if opts.NMSThreshold == 0 {
		opts.NMSThreshold = 0.45
	}

	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("read model %s: %w", modelPath, err)
	}
	if len(inputs) != 1 || len(outputs) != 1 {
		return nil, fmt.Errorf("expected 1 input and 1 output, got %d and %d", len(inputs), len(outputs))
	}
	inShape, outShape := inputs[0].Dimensions, outputs[0].Dimensions
	if len(inShape) != 4 || inShape[1] != 3 || len(outShape) != 3 {
		return nil, fmt.Errorf("unexpected model shapes: input %v, output %v", inShape, outShape)
	}

	y := &YOLOX{
		inH:     int(inShape[2]),
		inW:     int(inShape[3]),
		classes: int(outShape[2]) - 5,
		opts:    opts,
	}
	if want := y.anchorCount(); int(outShape[1]) != want {
		return nil, fmt.Errorf("output has %d anchors, expected %d for %dx%d input", outShape[1], want, y.inW, y.inH)
	}

	if y.input, err = ort.NewEmptyTensor[float32](ort.NewShape(1, 3, int64(y.inH), int64(y.inW))); err != nil {
		return nil, err
	}
	if y.output, err = ort.NewEmptyTensor[float32](ort.NewShape(outShape...)); err != nil {
		y.Close()
		return nil, err
	}

	so, err := ort.NewSessionOptions()
	if err != nil {
		y.Close()
		return nil, err
	}
	defer so.Destroy()
	if opts.Threads > 0 {
		if err := so.SetIntraOpNumThreads(opts.Threads); err != nil {
			y.Close()
			return nil, err
		}
	}

	y.session, err = ort.NewAdvancedSession(modelPath,
		[]string{inputs[0].Name}, []string{outputs[0].Name},
		[]ort.Value{y.input}, []ort.Value{y.output}, so)
	if err != nil {
		y.Close()
		return nil, fmt.Errorf("create session: %w", err)
	}
	return y, nil
}

// InputSize returns the model input width and height.
func (y *YOLOX) InputSize() (int, int) { return y.inW, y.inH }

// Close frees the session and tensors.
func (y *YOLOX) Close() {
	if y.session != nil {
		_ = y.session.Destroy()
	}
	if y.input != nil {
		_ = y.input.Destroy()
	}
	if y.output != nil {
		_ = y.output.Destroy()
	}
}

// Detect runs the model on img and returns detections with score >= minScore,
// sorted by score descending.
func (y *YOLOX) Detect(img image.Image, minScore float32) ([]Detection, error) {
	y.mu.Lock()
	defer y.mu.Unlock()

	ratio := y.preprocess(img)
	if err := y.session.Run(); err != nil {
		return nil, fmt.Errorf("inference: %w", err)
	}
	dets := y.postprocess(ratio, minScore, img.Bounds())
	return dets, nil
}

// Persons filters detections to the "person" class.
func Persons(dets []Detection) []Detection {
	var out []Detection
	for _, d := range dets {
		if d.Class == 0 {
			out = append(out, d)
		}
	}
	return out
}

var strides = [...]int{8, 16, 32}

func (y *YOLOX) anchorCount() int {
	n := 0
	for _, s := range strides {
		n += (y.inH / s) * (y.inW / s)
	}
	return n
}

// preprocess letterboxes img into the input tensor and returns the resize ratio.
// YOLOX letterbox: keep aspect ratio, place at top-left, pad with 114.
func (y *YOLOX) preprocess(img image.Image) float64 {
	src := toRGBA(img)
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	ratio := math.Min(float64(y.inW)/float64(sw), float64(y.inH)/float64(sh))
	nw, nh := int(float64(sw)*ratio), int(float64(sh)*ratio)

	data := y.input.GetData()
	plane := y.inW * y.inH

	// Channel order in the tensor: BGR (current YOLOX) or RGB (legacy).
	chIdx := [3]int{2, 1, 0} // tensor channel c takes source channel chIdx[c] (R=0,G=1,B=2)
	if y.opts.Legacy {
		chIdx = [3]int{0, 1, 2}
	}
	mean := [3]float32{0.485, 0.456, 0.406}
	std := [3]float32{0.229, 0.224, 0.225}

	norm := func(c int, v float32) float32 {
		if !y.opts.Legacy {
			return v
		}
		return (v/255 - mean[c]) / std[c]
	}

	for ty := 0; ty < y.inH; ty++ {
		for tx := 0; tx < y.inW; tx++ {
			i := ty*y.inW + tx
			if tx >= nw || ty >= nh {
				for c := 0; c < 3; c++ {
					data[c*plane+i] = norm(c, 114)
				}
				continue
			}
			px := sampleBilinear(src, (float64(tx)+0.5)/ratio-0.5, (float64(ty)+0.5)/ratio-0.5)
			for c := 0; c < 3; c++ {
				data[c*plane+i] = norm(c, px[chIdx[c]])
			}
		}
	}
	return ratio
}

func (y *YOLOX) postprocess(ratio float64, minScore float32, bounds image.Rectangle) []Detection {
	out := y.output.GetData()
	stride := 5 + y.classes
	var cands []Detection

	idx := 0
	for _, s := range strides {
		gh, gw := y.inH/s, y.inW/s
		for gy := 0; gy < gh; gy++ {
			for gx := 0; gx < gw; gx++ {
				row := out[idx*stride : (idx+1)*stride]
				idx++

				obj := row[4]
				if obj < minScore {
					continue // score = obj * cls <= obj
				}
				best, bestScore := 0, float32(0)
				for c := 0; c < y.classes; c++ {
					if v := row[5+c]; v > bestScore {
						best, bestScore = c, v
					}
				}
				score := obj * bestScore
				if score < minScore {
					continue
				}

				cx := (float64(row[0]) + float64(gx)) * float64(s)
				cy := (float64(row[1]) + float64(gy)) * float64(s)
				w := math.Exp(float64(row[2])) * float64(s)
				h := math.Exp(float64(row[3])) * float64(s)

				box := image.Rect(
					int((cx-w/2)/ratio), int((cy-h/2)/ratio),
					int((cx+w/2)/ratio), int((cy+h/2)/ratio),
				).Add(bounds.Min).Intersect(bounds)
				if box.Empty() {
					continue
				}
				cands = append(cands, Detection{Class: best, Label: label(best), Score: score, Box: box})
			}
		}
	}
	return nms(cands, y.opts.NMSThreshold)
}

// nms is class-aware non-maximum suppression.
func nms(dets []Detection, iouThr float32) []Detection {
	sort.Slice(dets, func(i, j int) bool { return dets[i].Score > dets[j].Score })
	keep := dets[:0:0]
	for _, d := range dets {
		ok := true
		for _, k := range keep {
			if k.Class == d.Class && iou(k.Box, d.Box) > iouThr {
				ok = false
				break
			}
		}
		if ok {
			keep = append(keep, d)
		}
	}
	return keep
}

func iou(a, b image.Rectangle) float32 {
	inter := a.Intersect(b)
	if inter.Empty() {
		return 0
	}
	ia := float32(inter.Dx() * inter.Dy())
	ua := float32(a.Dx()*a.Dy()+b.Dx()*b.Dy()) - ia
	return ia / ua
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	r := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(r, r.Bounds(), img, b.Min, draw.Src)
	return r
}

// sampleBilinear returns R, G, B (0-255) at fractional source position (x, y).
func sampleBilinear(img *image.RGBA, x, y float64) [3]float32 {
	b := img.Bounds()
	maxX, maxY := b.Dx()-1, b.Dy()-1
	x = math.Max(0, math.Min(x, float64(maxX)))
	y = math.Max(0, math.Min(y, float64(maxY)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, maxX), min(y0+1, maxY)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))

	at := func(px, py int) []uint8 {
		o := py*img.Stride + px*4
		return img.Pix[o : o+3]
	}
	p00, p10, p01, p11 := at(x0, y0), at(x1, y0), at(x0, y1), at(x1, y1)

	var out [3]float32
	for c := 0; c < 3; c++ {
		top := float32(p00[c])*(1-fx) + float32(p10[c])*fx
		bot := float32(p01[c])*(1-fx) + float32(p11[c])*fx
		out[c] = top*(1-fy) + bot*fy
	}
	return out
}
