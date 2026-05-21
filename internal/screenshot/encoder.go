package screenshot

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/disintegration/imaging"
	"github.com/screenshot-mcp-server/pkg/types"
)

// pngEncoder is shared across all PNG encodes. BestSpeed trades a modest
// increase in file size (~10-20%) for a 2-4x faster encode, which dominates
// capture latency for large frames. The default level is far slower and the
// extra compression rarely matters for screenshots streamed over MCP.
var pngEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

// ImageProcessor implements image processing and encoding operations
type ImageProcessor struct {
	defaultQuality int
	outputDir      string
}

// NewImageProcessor creates a new image processor
func NewImageProcessor() *ImageProcessor {
	return &ImageProcessor{
		defaultQuality: 95,
		outputDir:      "screenshots",
	}
}

// SetOutputDirectory sets the default output directory for saved files
func (p *ImageProcessor) SetOutputDirectory(dir string) {
	p.outputDir = dir
}

// Encode converts a ScreenshotBuffer to the specified format
func (p *ImageProcessor) Encode(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int) ([]byte, error) {
	if buffer == nil {
		return nil, fmt.Errorf("buffer cannot be nil")
	}

	// Convert buffer to image.Image
	img, err := p.ToImage(buffer)
	if err != nil {
		return nil, fmt.Errorf("failed to convert buffer to image: %w", err)
	}

	if format == types.FormatAuto {
		format = chooseFormat(img)
	}
	return p.encodeImage(img, format, quality)
}

// EncodeScaled is like Encode but optionally crops to a region and downscales
// the image before encoding, and resolves FormatAuto by sampling the image.
//
// Pipeline order: convert → crop (region) → downscale (scale / max_width) →
// encode. Cropping and downscaling both shrink the encode cost and payload —
// the highest-leverage knobs for agents that only need part of a window, or
// only need to "see" it rather than read it at full resolution.
//
//   - region (optional) selects a sub-rectangle in capture pixels; it is
//     clamped to the image bounds.
//   - the downscale target is the smaller of scale (a 0<scale<1 multiplier) and
//     max_width (a hard cap on width). The image is never upscaled.
//
// The returned width/height are the dimensions actually encoded, and chosen is
// the concrete format used (resolved from FormatAuto when applicable).
func (p *ImageProcessor) EncodeScaled(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int, region *types.Rectangle, maxWidth int, scale float64) (data []byte, width, height int, chosen types.ImageFormat, err error) {
	if buffer == nil {
		return nil, 0, 0, "", fmt.Errorf("buffer cannot be nil")
	}
	img, err := p.ToImage(buffer)
	if err != nil {
		return nil, 0, 0, "", fmt.Errorf("failed to convert buffer to image: %w", err)
	}

	if region != nil {
		crop := image.Rect(region.X, region.Y, region.X+region.Width, region.Y+region.Height).
			Intersect(img.Bounds())
		if crop.Empty() {
			return nil, 0, 0, "", fmt.Errorf("region %dx%d at (%d,%d) lies outside the %dx%d capture",
				region.Width, region.Height, region.X, region.Y, buffer.Width, buffer.Height)
		}
		img = imaging.Crop(img, crop)
	}

	width, height = img.Bounds().Dx(), img.Bounds().Dy()
	if nw, nh, resize := targetSize(width, height, scale, maxWidth); resize {
		// Linear (bilinear) balances speed against legibility for downscaling;
		// the resize is cheap relative to the encode it shrinks.
		img = imaging.Resize(img, nw, nh, imaging.Linear)
		width, height = nw, nh
	}

	chosen = format
	if chosen == types.FormatAuto {
		chosen = chooseFormat(img)
	}
	data, err = p.encodeImage(img, chosen, quality)
	return data, width, height, chosen, err
}

// chooseFormat picks an encoding format by sampling the image's colour
// diversity. Photographic / video content has most pixels unique, so JPEG is
// far smaller and faster; flat UI content repeats a small palette, where PNG is
// smaller and keeps text crisp. The sample is a coarse grid, so the cost is
// negligible relative to the encode it informs.
func chooseFormat(img image.Image) types.ImageFormat {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return types.FormatPNG
	}
	const grid = 96 // up to grid*grid samples
	stepX, stepY := w/grid, h/grid
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}
	seen := make(map[uint32]struct{}, grid*grid)
	samples := 0
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA() // 16-bit per channel
			key := uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(bl>>8)
			seen[key] = struct{}{}
			samples++
		}
	}
	if samples == 0 {
		return types.FormatPNG
	}
	// Photographic content tends well above this unique-colour ratio; flat UI
	// sits well below it.
	if float64(len(seen))/float64(samples) > 0.5 {
		return types.FormatJPEG
	}
	return types.FormatPNG
}

// targetSize returns the dimensions to downscale to and whether a resize is
// needed. scale (0<scale<1) and maxWidth combine by taking the smaller factor;
// the image is never upscaled.
func targetSize(w, h int, scale float64, maxWidth int) (int, int, bool) {
	s := 1.0
	if scale > 0 && scale < 1 {
		s = scale
	}
	if maxWidth > 0 && w > maxWidth {
		if ms := float64(maxWidth) / float64(w); ms < s {
			s = ms
		}
	}
	if s >= 1.0 || w <= 0 || h <= 0 {
		return w, h, false
	}
	nw := int(math.Round(float64(w) * s))
	nh := int(math.Round(float64(h) * s))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh, true
}

// encodeImage encodes an already-prepared image to the requested format.
func (p *ImageProcessor) encodeImage(img image.Image, format types.ImageFormat, quality int) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case types.FormatPNG:
		err = pngEncoder.Encode(&buf, img)
	case types.FormatJPEG:
		if quality <= 0 || quality > 100 {
			quality = p.defaultQuality
		}
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	case types.FormatBMP:
		// For BMP, we'll use PNG as fallback since Go doesn't have native BMP support
		// In a production system, you might want to add a BMP encoder library
		err = pngEncoder.Encode(&buf, img)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to encode image: %w", err)
	}

	return buf.Bytes(), nil
}

// EncodeToBase64 encodes an image buffer to base64 string
func (p *ImageProcessor) EncodeToBase64(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int) (string, error) {
	data, err := p.Encode(buffer, format, quality)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// EncodeToWriter writes encoded image data to an io.Writer
func (p *ImageProcessor) EncodeToWriter(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int, writer io.Writer) error {
	data, err := p.Encode(buffer, format, quality)
	if err != nil {
		return err
	}

	_, err = writer.Write(data)
	return err
}

// SaveToFile saves the screenshot buffer to a file
func (p *ImageProcessor) SaveToFile(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int, filename string) error {
	// Create output directory if it doesn't exist
	dir := filepath.Dir(filename)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create and open file
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", filename, err)
	}
	defer file.Close()

	// Encode and write to file
	return p.EncodeToWriter(buffer, format, quality, file)
}

// SaveWithTimestamp saves the screenshot with a timestamp-based filename
func (p *ImageProcessor) SaveWithTimestamp(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int, prefix string) (string, error) {
	// Ensure output directory exists
	if err := os.MkdirAll(p.outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate filename with timestamp
	timestamp := time.Now().Format("20060102_150405")
	var ext string
	switch format {
	case types.FormatPNG:
		ext = "png"
	case types.FormatJPEG:
		ext = "jpg"
	case types.FormatBMP:
		ext = "bmp"
	default:
		ext = "png"
	}

	filename := fmt.Sprintf("%s_%s.%s", prefix, timestamp, ext)
	filepath := filepath.Join(p.outputDir, filename)

	err := p.SaveToFile(buffer, format, quality, filepath)
	return filepath, err
}

// Decode converts image data to a ScreenshotBuffer
func (p *ImageProcessor) Decode(data []byte) (*types.ScreenshotBuffer, error) {
	// Decode the image
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Convert to RGBA if needed
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(img.Bounds())
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				rgba.Set(x, y, img.At(x, y))
			}
		}
	}

	// Create screenshot buffer
	bounds := rgba.Bounds()
	buffer := &types.ScreenshotBuffer{
		Data:      rgba.Pix,
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
		Stride:    rgba.Stride,
		Format:    "RGBA32",
		DPI:       96, // Default DPI
		Timestamp: time.Now(),
		SourceRect: types.Rectangle{
			X:      bounds.Min.X,
			Y:      bounds.Min.Y,
			Width:  bounds.Dx(),
			Height: bounds.Dy(),
		},
	}

	return buffer, nil
}

// Resize resizes the image buffer to the specified dimensions
func (p *ImageProcessor) Resize(buffer *types.ScreenshotBuffer, width, height int) (*types.ScreenshotBuffer, error) {
	// Convert to image.Image
	img, err := p.ToImage(buffer)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to image: %w", err)
	}

	// Resize using imaging library
	resized := imaging.Resize(img, width, height, imaging.Lanczos)

	// Convert back to buffer
	return p.imageToBuffer(resized), nil
}

// Crop crops the image buffer to the specified rectangle
func (p *ImageProcessor) Crop(buffer *types.ScreenshotBuffer, rect types.Rectangle) (*types.ScreenshotBuffer, error) {
	// Convert to image.Image
	img, err := p.ToImage(buffer)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to image: %w", err)
	}

	// Define crop rectangle
	cropRect := image.Rect(rect.X, rect.Y, rect.X+rect.Width, rect.Y+rect.Height)

	// Ensure crop rectangle is within bounds
	bounds := img.Bounds()
	cropRect = cropRect.Intersect(bounds)
	if cropRect.Empty() {
		return nil, fmt.Errorf("crop rectangle is outside image bounds")
	}

	// Crop using imaging library
	cropped := imaging.Crop(img, cropRect)

	// Convert back to buffer
	return p.imageToBuffer(cropped), nil
}

// ToImage converts a ScreenshotBuffer to image.Image
func (p *ImageProcessor) ToImage(buffer *types.ScreenshotBuffer) (image.Image, error) {
	if buffer == nil {
		return nil, fmt.Errorf("buffer cannot be nil")
	}

	var img image.Image

	switch buffer.Format {
	case "BGRA32":
		// Windows screenshots are typically BGRA
		img = p.bgraToRGBA(buffer)
	case "RGBA32":
		// Create RGBA image
		rgba := &image.RGBA{
			Pix:    buffer.Data,
			Stride: buffer.Stride,
			Rect:   image.Rect(0, 0, buffer.Width, buffer.Height),
		}
		img = rgba
	case "PNG", "JPEG", "BMP":
		// Already encoded data, decode it first
		decoded, err := p.Decode(buffer.Data)
		if err != nil {
			return nil, err
		}
		return p.ToImage(decoded)
	default:
		return nil, fmt.Errorf("unsupported buffer format: %s", buffer.Format)
	}

	return img, nil
}

// bgraToRGBA converts BGRA data to RGBA in place and returns an image that
// wraps the buffer's own pixel slice — no second multi-megabyte buffer is
// allocated, which removes the per-capture allocation and the GC pressure it
// creates during a burst. Only the B and R channels are swapped (G and A are
// already in position). The buffer's Format is flipped to RGBA32 afterwards so
// a repeat conversion of the same buffer is a no-op rather than swapping the
// channels back. Callers therefore must treat buffer.Data as consumed once it
// has been encoded.
func (p *ImageProcessor) bgraToRGBA(buffer *types.ScreenshotBuffer) image.Image {
	pix := buffer.Data
	n := len(pix)
	n -= n % 4
	pix = pix[:n]
	for i := 0; i < n; i += 4 {
		pix[i], pix[i+2] = pix[i+2], pix[i] // swap B <-> R
	}

	buffer.Format = "RGBA32"
	return &image.RGBA{
		Pix:    buffer.Data,
		Stride: buffer.Stride,
		Rect:   image.Rect(0, 0, buffer.Width, buffer.Height),
	}
}

// imageToBuffer converts an image.Image back to ScreenshotBuffer
func (p *ImageProcessor) imageToBuffer(img image.Image) *types.ScreenshotBuffer {
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)

	// Copy image data to RGBA
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}

	return &types.ScreenshotBuffer{
		Data:      rgba.Pix,
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
		Stride:    rgba.Stride,
		Format:    "RGBA32",
		DPI:       96,
		Timestamp: time.Now(),
		SourceRect: types.Rectangle{
			X:      bounds.Min.X,
			Y:      bounds.Min.Y,
			Width:  bounds.Dx(),
			Height: bounds.Dy(),
		},
	}
}

// GetImageInfo returns basic information about image data
func (p *ImageProcessor) GetImageInfo(data []byte) (*ImageInfo, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image config: %w", err)
	}

	return &ImageInfo{
		Width:      config.Width,
		Height:     config.Height,
		Format:     format,
		ColorModel: config.ColorModel,
		Size:       len(data),
	}, nil
}

// ImageInfo contains metadata about an image
type ImageInfo struct {
	Width      int
	Height     int
	Format     string
	ColorModel color.Model
	Size       int
}

// FileSystemStorage provides file-based storage with organized directory structure
type FileSystemStorage struct {
	baseDir    string
	processor  *ImageProcessor
	dateFormat string
}

// NewFileSystemStorage creates a new file system storage handler
func NewFileSystemStorage(baseDir string) *FileSystemStorage {
	return &FileSystemStorage{
		baseDir:    baseDir,
		processor:  NewImageProcessor(),
		dateFormat: "2006/01/02", // YYYY/MM/DD
	}
}

// Save saves a screenshot with organized directory structure
func (fs *FileSystemStorage) Save(buffer *types.ScreenshotBuffer, format types.ImageFormat, quality int, name string) (string, error) {
	// Create date-based directory structure
	now := time.Now()
	dateDir := now.Format(fs.dateFormat)
	fullDir := filepath.Join(fs.baseDir, dateDir)

	// Ensure directory exists
	if err := os.MkdirAll(fullDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory structure: %w", err)
	}

	// Generate filename
	timestamp := now.Format("150405") // HHMMSS
	var ext string
	switch format {
	case types.FormatPNG:
		ext = "png"
	case types.FormatJPEG:
		ext = "jpg"
	case types.FormatBMP:
		ext = "bmp"
	default:
		ext = "png"
	}

	filename := fmt.Sprintf("%s_%s.%s", name, timestamp, ext)
	fullPath := filepath.Join(fullDir, filename)

	// Save the file
	err := fs.processor.SaveToFile(buffer, format, quality, fullPath)
	if err != nil {
		return "", err
	}

	return fullPath, nil
}

// Ensure ImageProcessor implements the interface
var _ types.ImageProcessor = (*ImageProcessor)(nil)
