package image

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ---------------------------------------------------------------------------
// image.info — get image metadata
// ---------------------------------------------------------------------------

// ImageInfoNode reads image metadata without modifying the image.
// Type: "image.info"
type ImageInfoNode struct{}

func (n *ImageInfoNode) Type() string { return "image.info" }

// PublishesToThirdParty: image.info sends nothing off this machine (issue #1418).
func (ImageInfoNode) PublishesToThirdParty() bool { return false }

func (n *ImageInfoNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.info: %w", err)
		}
		if imgPath == "" {
			outItems = append(outItems, workflow.Item{JSON: newJSON})
			continue
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.info: open %q: %w", imgPath, err)
		}
		b := img.Bounds()
		fi, _ := os.Stat(imgPath)
		var sizeBytes int64
		if fi != nil {
			sizeBytes = fi.Size()
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(imgPath), "."))

		newJSON["image_width"] = b.Dx()
		newJSON["image_height"] = b.Dy()
		newJSON["image_format"] = ext
		newJSON["image_size_bytes"] = sizeBytes
		newJSON["image_aspect_ratio"] = fmt.Sprintf("%.4f", float64(b.Dx())/float64(b.Dy()))
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// ---------------------------------------------------------------------------
// image.resize — resize to given dimensions
// ---------------------------------------------------------------------------

// ImageResizeNode resizes an image to the specified width and height.
// fit modes:
//   - "contain" (default): fit within bounds, preserve aspect ratio (may add padding if fill_color set)
//   - "cover": fill bounds, crop to center
//   - "fill": stretch to exact size
//   - "width": set width only, auto height
//   - "height": set height only, auto width
//
// Type: "image.resize"
type ImageResizeNode struct{}

func (n *ImageResizeNode) Type() string { return "image.resize" }

// PublishesToThirdParty: image.resize sends nothing off this machine (issue #1418).
func (ImageResizeNode) PublishesToThirdParty() bool { return false }

func (n *ImageResizeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	width := intConfig(config, "width", 0)
	height := intConfig(config, "height", 0)
	fit, _ := config["fit"].(string)
	if fit == "" {
		fit = "contain"
	}
	outputField, _ := config["output_field"].(string)
	if outputField == "" {
		outputField = "image_path"
	}
	outputDir, _ := config["output_dir"].(string)

	if width == 0 && height == 0 {
		return nil, fmt.Errorf("image.resize: at least one of width or height must be set")
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.resize: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.resize: no image path found in item")
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.resize: open %q: %w", imgPath, err)
		}

		var result image.Image
		switch fit {
		case "cover":
			result = imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)
		case "fill":
			result = imaging.Resize(img, width, height, imaging.Lanczos)
		case "width":
			result = imaging.Resize(img, width, 0, imaging.Lanczos)
		case "height":
			result = imaging.Resize(img, 0, height, imaging.Lanczos)
		default: // "contain"
			result = imaging.Fit(img, width, height, imaging.Lanczos)
		}

		outFile, err := buildOutputPath(ctx, imgPath, outputDir, "resized", "")
		if err != nil {
			return nil, fmt.Errorf("image.resize: %w", err)
		}
		if err := imaging.Save(result, outFile); err != nil {
			return nil, fmt.Errorf("image.resize: save %q: %w", outFile, err)
		}
		newJSON[outputField] = outFile
		newJSON["image_width"] = result.Bounds().Dx()
		newJSON["image_height"] = result.Bounds().Dy()
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// ---------------------------------------------------------------------------
// image.crop — crop to region or aspect ratio
// ---------------------------------------------------------------------------

// ImageCropNode crops an image.
//
// Config modes:
//   - Explicit region: x, y, width, height (pixels)
//   - Aspect ratio: aspect_ratio ("1:1", "16:9", "4:3"), anchor ("center","top","bottom","left","right")
//
// Type: "image.crop"
type ImageCropNode struct{}

func (n *ImageCropNode) Type() string { return "image.crop" }

// PublishesToThirdParty: image.crop sends nothing off this machine (issue #1418).
func (ImageCropNode) PublishesToThirdParty() bool { return false }

func (n *ImageCropNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	x := intConfig(config, "x", -1)
	y := intConfig(config, "y", -1)
	width := intConfig(config, "width", 0)
	height := intConfig(config, "height", 0)
	aspectRatio, _ := config["aspect_ratio"].(string)
	anchor, _ := config["anchor"].(string)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	if outputField == "" {
		outputField = "image_path"
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.crop: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.crop: no image path found in item")
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.crop: open %q: %w", imgPath, err)
		}
		b := img.Bounds()
		srcW, srcH := b.Dx(), b.Dy()

		var result image.Image

		if aspectRatio != "" {
			// Parse ratio e.g. "16:9"
			var ratioW, ratioH float64
			if _, err := fmt.Sscanf(strings.ReplaceAll(aspectRatio, ":", " "), "%f %f", &ratioW, &ratioH); err != nil || ratioH == 0 {
				return nil, fmt.Errorf("image.crop: invalid aspect_ratio %q (use e.g. '16:9')", aspectRatio)
			}
			targetRatio := ratioW / ratioH
			srcRatio := float64(srcW) / float64(srcH)

			var cropW, cropH int
			if srcRatio > targetRatio {
				// Source is wider — crop width
				cropH = srcH
				cropW = int(math.Round(float64(srcH) * targetRatio))
			} else {
				// Source is taller — crop height
				cropW = srcW
				cropH = int(math.Round(float64(srcW) / targetRatio))
			}

			anchorPoint := imaging.Center
			switch strings.ToLower(anchor) {
			case "top":
				anchorPoint = imaging.Top
			case "bottom":
				anchorPoint = imaging.Bottom
			case "left":
				anchorPoint = imaging.Left
			case "right":
				anchorPoint = imaging.Right
			case "topleft", "top_left":
				anchorPoint = imaging.TopLeft
			case "topright", "top_right":
				anchorPoint = imaging.TopRight
			}
			result = imaging.CropAnchor(img, cropW, cropH, anchorPoint)
		} else if x >= 0 && y >= 0 && width > 0 && height > 0 {
			rect := image.Rect(x, y, x+width, y+height)
			result = imaging.Crop(img, rect)
		} else {
			return nil, fmt.Errorf("image.crop: provide either (x,y,width,height) or aspect_ratio")
		}

		outFile, err := buildOutputPath(ctx, imgPath, outputDir, "cropped", "")
		if err != nil {
			return nil, fmt.Errorf("image.crop: %w", err)
		}
		if err := imaging.Save(result, outFile); err != nil {
			return nil, fmt.Errorf("image.crop: save %q: %w", outFile, err)
		}
		newJSON[outputField] = outFile
		newJSON["image_width"] = result.Bounds().Dx()
		newJSON["image_height"] = result.Bounds().Dy()
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// ---------------------------------------------------------------------------
// image.thumbnail — resize + crop to exact dimensions
// ---------------------------------------------------------------------------

// ImageThumbnailNode produces an exact-size image by resizing and center-cropping.
// Perfect for social media posts that require specific pixel dimensions.
// Type: "image.thumbnail"
type ImageThumbnailNode struct{}

func (n *ImageThumbnailNode) Type() string { return "image.thumbnail" }

// PublishesToThirdParty: image.thumbnail sends nothing off this machine (issue #1418).
func (ImageThumbnailNode) PublishesToThirdParty() bool { return false }

func (n *ImageThumbnailNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	width := intConfig(config, "width", 0)
	height := intConfig(config, "height", 0)
	anchor, _ := config["anchor"].(string)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	if outputField == "" {
		outputField = "image_path"
	}
	if width == 0 || height == 0 {
		return nil, fmt.Errorf("image.thumbnail: both width and height are required")
	}

	anchorPoint := imaging.Center
	switch strings.ToLower(anchor) {
	case "top":
		anchorPoint = imaging.Top
	case "bottom":
		anchorPoint = imaging.Bottom
	case "left":
		anchorPoint = imaging.Left
	case "right":
		anchorPoint = imaging.Right
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.thumbnail: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.thumbnail: no image path found in item")
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.thumbnail: open %q: %w", imgPath, err)
		}

		result := imaging.Fill(img, width, height, anchorPoint, imaging.Lanczos)
		outFile, err := buildOutputPath(ctx, imgPath, outputDir, "thumb", "")
		if err != nil {
			return nil, fmt.Errorf("image.thumbnail: %w", err)
		}
		if err := imaging.Save(result, outFile); err != nil {
			return nil, fmt.Errorf("image.thumbnail: save %q: %w", outFile, err)
		}
		newJSON[outputField] = outFile
		newJSON["image_width"] = width
		newJSON["image_height"] = height
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// ---------------------------------------------------------------------------
// image.convert — change format and/or quality
// ---------------------------------------------------------------------------

// ImageConvertNode converts an image to a different format.
// Supported formats: jpeg, jpg, png, gif, tiff, bmp, webp (decode only for webp).
// Type: "image.convert"
type ImageConvertNode struct{}

func (n *ImageConvertNode) Type() string { return "image.convert" }

// PublishesToThirdParty: image.convert sends nothing off this machine (issue #1418).
func (ImageConvertNode) PublishesToThirdParty() bool { return false }

func (n *ImageConvertNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	format, _ := config["format"].(string)
	if format == "" {
		return nil, fmt.Errorf("image.convert: format is required (jpeg, png, gif, tiff, bmp)")
	}
	format = strings.ToLower(strings.TrimPrefix(format, "."))
	if format == "jpg" {
		format = "jpeg"
	}
	quality := intConfig(config, "quality", 85)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	if outputField == "" {
		outputField = "image_path"
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.convert: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.convert: no image path found in item")
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.convert: open %q: %w", imgPath, err)
		}

		outFile, err := buildOutputPath(ctx, imgPath, outputDir, "converted", format)
		if err != nil {
			return nil, fmt.Errorf("image.convert: %w", err)
		}
		saveOpts := []imaging.EncodeOption{}
		if format == "jpeg" {
			saveOpts = append(saveOpts, imaging.JPEGQuality(quality))
		}
		if err := imaging.Save(img, outFile, saveOpts...); err != nil {
			return nil, fmt.Errorf("image.convert: save %q: %w", outFile, err)
		}

		fi, _ := os.Stat(outFile)
		var sizeBytes int64
		if fi != nil {
			sizeBytes = fi.Size()
		}
		newJSON[outputField] = outFile
		newJSON["image_format"] = format
		newJSON["image_size_bytes"] = sizeBytes
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// ---------------------------------------------------------------------------
// image.adjust — brightness, contrast, saturation, sharpness, blur, gamma
// ---------------------------------------------------------------------------

// ImageAdjustNode applies color/tone adjustments to an image.
//
// All adjustment values are in the range [-100, 100] except:
//   - gamma: float, default 1.0 (>1 = brighter, <1 = darker)
//   - blur: sigma in pixels (0 = no blur)
//   - sharpen: sigma in pixels (0 = no sharpening)
//
// Type: "image.adjust"
type ImageAdjustNode struct{}

func (n *ImageAdjustNode) Type() string { return "image.adjust" }

// PublishesToThirdParty: image.adjust sends nothing off this machine (issue #1418).
func (ImageAdjustNode) PublishesToThirdParty() bool { return false }

func (n *ImageAdjustNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	brightness := floatConfig(config, "brightness", 0)
	contrast := floatConfig(config, "contrast", 0)
	saturation := floatConfig(config, "saturation", 0)
	sharpen := floatConfig(config, "sharpen", 0)
	blur := floatConfig(config, "blur", 0)
	gamma := floatConfig(config, "gamma", 1.0)
	grayscale, _ := config["grayscale"].(bool)
	sepia, _ := config["sepia"].(bool)
	invert, _ := config["invert"].(bool)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	if outputField == "" {
		outputField = "image_path"
	}

	outItems := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)
		imgPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.adjust: %w", err)
		}
		if imgPath == "" {
			return nil, fmt.Errorf("image.adjust: no image path found in item")
		}

		img, err := openImageSafely(imgPath)
		if err != nil {
			return nil, fmt.Errorf("image.adjust: open %q: %w", imgPath, err)
		}

		result := img

		if brightness != 0 {
			result = imaging.AdjustBrightness(result, brightness)
		}
		if contrast != 0 {
			result = imaging.AdjustContrast(result, contrast)
		}
		if saturation != 0 {
			result = imaging.AdjustSaturation(result, saturation)
		}
		if gamma != 1.0 && gamma > 0 {
			result = imaging.AdjustGamma(result, gamma)
		}
		if sharpen > 0 {
			result = imaging.Sharpen(result, sharpen)
		}
		if blur > 0 {
			result = imaging.Blur(result, blur)
		}
		if grayscale {
			result = imaging.Grayscale(result)
		}
		if invert {
			result = imaging.Invert(result)
		}
		if sepia {
			// Grayscale then apply sepia tone.
			result = imaging.Grayscale(result)
			result = imaging.AdjustFunc(result, func(c color.NRGBA) color.NRGBA {
				r := float64(c.R)
				g := float64(c.G)
				b := float64(c.B)
				nr := clamp(r*0.393 + g*0.769 + b*0.189)
				ng := clamp(r*0.349 + g*0.686 + b*0.168)
				nb := clamp(r*0.272 + g*0.534 + b*0.131)
				return color.NRGBA{R: uint8(nr), G: uint8(ng), B: uint8(nb), A: c.A}
			})
		}

		outFile, err := buildOutputPath(ctx, imgPath, outputDir, "adjusted", "")
		if err != nil {
			return nil, fmt.Errorf("image.adjust: %w", err)
		}
		if err := imaging.Save(result, outFile); err != nil {
			return nil, fmt.Errorf("image.adjust: save %q: %w", outFile, err)
		}
		newJSON[outputField] = outFile
		outItems = append(outItems, workflow.Item{JSON: newJSON})
	}
	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
