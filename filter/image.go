package filter

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os/exec"
	"strings"

	"golang.org/x/image/draw"
)

// resizeImage resizes an image so its largest dimension is at most maxDim.
// Returns JPEG-encoded bytes for efficiency.
func resizeImage(data []byte, mimeType string, maxDim int) ([]byte, error) {
	reader := bytes.NewReader(data)

	var img image.Image
	var err error

	switch {
	case strings.Contains(mimeType, "png"):
		img, err = png.Decode(reader)
	case strings.Contains(mimeType, "jpeg") || strings.Contains(mimeType, "jpg"):
		img, err = jpeg.Decode(reader)
	default:
		img, _, err = image.Decode(reader)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w <= maxDim && h <= maxDim {
		return encodeJPEG(img)
	}

	var newW, newH int
	if w > h {
		newW = maxDim
		newH = h * maxDim / w
	} else {
		newH = maxDim
		newW = w * maxDim / h
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	return encodeJPEG(dst)
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// ocrImage tries to extract text from an image using tesseract.
// Returns empty string if tesseract is not available or fails — never errors.
func ocrImage(imageData []byte) string {
	tesseract, err := exec.LookPath("tesseract")
	if err != nil {
		return ""
	}

	// tesseract reads from stdin with "-" and outputs to stdout with "stdout"
	cmd := exec.Command(tesseract, "stdin", "stdout", "--psm", "6", "-l", "eng")
	cmd.Stdin = bytes.NewReader(imageData)

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	text := strings.TrimSpace(string(out))
	// If tesseract produced very little, it's probably garbage
	if len(text) < 5 {
		return ""
	}
	return text
}

// DetectMIME returns the MIME type based on magic bytes.
func DetectMIME(data []byte) string {
	if len(data) < 4 {
		return "application/octet-stream"
	}
	if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "application/octet-stream"
}
