package image

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ImageEditNode edits images in pure Go — the counterpart of n8n's Edit
// Image node: blur, border, composite, create, crop, draw, information,
// multi_step, resize, rotate, shear, text and transparent.
//
// Images come either as a binary property (an item JSON field holding
// base64, default "data", with file_name/mime_type/... metadata, or bytes in
// Item.Binary) and go back out the same way, or — when config "field" is set
// or the item only carries image_path/path/file_path/media_path — as a file
// path, with the result written to a new file like image.resize does.
//
// Type: "image.edit"
type ImageEditNode struct{}

func (n *ImageEditNode) Type() string { return "image.edit" }

// PublishesToThirdParty: image.edit sends nothing off this machine (issue #1418).
func (ImageEditNode) PublishesToThirdParty() bool { return false }

// editSettings is image.edit's parsed config.
type editSettings struct {
	Operation   string
	Ops         []editOp
	Prop        string // data_property_name: input binary property
	Dest        string // destination_key: output binary property
	Field       string // path mode: item field holding the image path
	OutputDir   string
	OutputField string
	FileName    string
	Format      string
	Quality     int
}

func parseEditSettings(config map[string]interface{}) (editSettings, error) {
	s := editSettings{
		Prop:        strings.TrimSpace(cfgStr(config, "data_property_name", defaultBinaryProp)),
		Field:       strings.TrimSpace(cfgStr(config, "field", "")),
		OutputDir:   strings.TrimSpace(cfgStr(config, "output_dir", "")),
		OutputField: strings.TrimSpace(cfgStr(config, "output_field", "image_path")),
		FileName:    strings.TrimSpace(cfgStr(config, "file_name", "")),
		Format:      strings.TrimSpace(cfgStr(config, "format", "")),
	}
	s.Dest = strings.TrimSpace(cfgStr(config, "destination_key", s.Prop))
	op, err := cfgChoice(config, "operation", opBorder, editOperations...)
	if err != nil {
		return s, err
	}
	s.Operation = op
	mode := hexAlpha
	if m, err := cfgChoice(config, "hex_alpha", "alpha", "alpha", "opacity"); err != nil {
		return s, err
	} else if m == "opacity" {
		mode = hexOpacity
	}
	if s.Quality, err = cfgInt(config, "quality", 0); err != nil {
		return s, err
	}
	if s.Quality < 0 || s.Quality > 100 {
		return s, fmt.Errorf("quality: must be between 0 and 100")
	}
	if _, _, _, err := outputFormat(s.Format, "png"); err != nil {
		return s, err
	}
	if s.Ops, err = parseOperations(config, op, mode); err != nil {
		return s, err
	}
	if font := strings.TrimSpace(cfgStr(config, "font", "")); font != "" {
		for i := range s.Ops { // the node-level font option wins over a step's own
			if s.Ops[i].Name == opText {
				s.Ops[i].Font = font
			}
		}
	}
	return s, nil
}

func (n *ImageEditNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	s, err := parseEditSettings(config)
	if err != nil {
		return nil, fmt.Errorf("image.edit: %w", err)
	}
	out := make([]workflow.Item, 0, len(input.Items))
	for i, item := range input.Items {
		res, err := editItem(ctx, s, item)
		if err != nil {
			return nil, fmt.Errorf("image.edit: item %d: %w", i, err)
		}
		out = append(out, res)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// editSource is where one item's image came from.
type editSource struct {
	pathMode bool
	path     string // path mode: the input file ("" for create)
	size     int    // encoded input size in bytes
	meta     fileMeta
	dec      *decodedImage // nil for create
}

func loadSource(ctx context.Context, s editSettings, item workflow.Item) (*editSource, error) {
	create := len(s.Ops) > 0 && s.Ops[0].Name == opCreate
	src := &editSource{pathMode: s.Field != ""}
	if !src.pathMode && !create && !hasBinary(item, s.Prop) && resolveImageField(item.JSON, "") != "" {
		src.pathMode = true
	}
	if create {
		src.pathMode = src.pathMode || s.OutputDir != ""
		return src, nil
	}
	var data []byte
	if src.pathMode {
		p, err := imagePath(ctx, item.JSON, s.Field)
		if err != nil {
			return nil, err
		}
		if p == "" {
			return nil, fmt.Errorf("no image path in field %q (or image_path, path, file_path, media_path)", s.Field)
		}
		if data, err = readImageFile(p); err != nil {
			return nil, err
		}
		src.path = p
		src.meta = fileMeta{FileName: filepath.Base(p)}
	} else {
		if !hasBinary(item, s.Prop) {
			return nil, fmt.Errorf("data_property_name: the item has no binary property %q and no image path field", s.Prop)
		}
		var err error
		if data, src.meta, err = readBinaryProp(item, s.Prop); err != nil {
			return nil, err
		}
	}
	dec, err := decodeImageBytes(data)
	if err != nil {
		return nil, err
	}
	src.dec, src.size = dec, len(data)
	return src, nil
}

func editItem(ctx context.Context, s editSettings, item workflow.Item) (workflow.Item, error) {
	src, err := loadSource(ctx, s, item)
	if err != nil {
		return workflow.Item{}, err
	}
	j := copyMap(item.JSON)
	if s.Operation == opInformation {
		for k, v := range imageInfo(src.dec, src.size) {
			j[k] = v
		}
		return workflow.Item{JSON: j, Binary: item.Binary}, nil
	}

	var img *image.NRGBA
	inFormat := "png"
	if src.dec != nil {
		img, inFormat = src.dec.Img, src.dec.Format
	}
	for i, op := range s.Ops {
		if img, err = applyOp(ctx, img, op, item, s, src.pathMode); err != nil {
			if len(s.Ops) > 1 {
				return workflow.Item{}, fmt.Errorf("operations[%d] (%s): %w", i, op.Name, err)
			}
			return workflow.Item{}, fmt.Errorf("%s: %w", op.Name, err)
		}
	}

	format, ext, note, _ := outputFormat(s.Format, inFormat)
	data, err := encodeImage(img, format, s.Quality)
	if err != nil {
		return workflow.Item{}, fmt.Errorf("encode %s: %w", ext, err)
	}
	if note != "" {
		j["image_edit_warning"] = note
	}
	if src.pathMode {
		out, err := writeEditedFile(ctx, s, src.path, ext, data)
		if err != nil {
			return workflow.Item{}, err
		}
		j[s.OutputField] = out
		j["image_width"], j["image_height"] = img.Bounds().Dx(), img.Bounds().Dy()
		j["image_format"], j["image_size_bytes"] = ext, len(data)
		return workflow.Item{JSON: j, Binary: item.Binary}, nil
	}

	writeBinaryProp(j, s.Dest, data, outputMeta(s, src.meta, ext))
	var bin map[string][]byte
	for k, v := range item.Binary { // the edited image now lives in the JSON field
		if k != s.Dest {
			if bin == nil {
				bin = map[string][]byte{}
			}
			bin[k] = v
		}
	}
	return workflow.Item{JSON: j, Binary: bin}, nil
}

// applyOp runs one operation; img is nil only before a leading create.
func applyOp(ctx context.Context, img *image.NRGBA, op editOp, item workflow.Item, s editSettings, pathMode bool) (*image.NRGBA, error) {
	switch op.Name {
	case opCreate:
		return createCanvas(op)
	case opBlur:
		return blurImage(img, op), nil
	case opBorder:
		return borderImage(img, op)
	case opCrop:
		return cropImage(img, op)
	case opResize:
		return resizeImage(img, op)
	case opRotate:
		return rotateImage(img, op)
	case opShear:
		return shearImage(img, op)
	case opDraw:
		drawShape(img, op)
		return img, nil
	case opTransparent:
		makeTransparent(img, op)
		return img, nil
	case opText:
		return img, drawText(ctx, img, op)
	case opComposite:
		overlay, err := loadOverlay(ctx, item, op.CompositeProp, pathMode)
		if err != nil {
			return nil, fmt.Errorf("data_property_name_composite: %w", err)
		}
		return img, compositeImage(img, overlay, op)
	}
	return nil, fmt.Errorf("operation %q has no implementation", op.Name)
}

// loadOverlay reads the image to composite: a binary property, or in path
// mode a JSON field holding a file path.
func loadOverlay(ctx context.Context, item workflow.Item, prop string, pathMode bool) (*image.NRGBA, error) {
	var data []byte
	if p, isStr := item.JSON[prop].(string); pathMode && isStr {
		p, err := fsconfine.Path(ctx, expandHome(p))
		if err != nil {
			return nil, err
		}
		if data, err = readImageFile(p); err != nil {
			return nil, err
		}
	} else {
		if !hasBinary(item, prop) {
			return nil, fmt.Errorf("the item has no binary property %q", prop)
		}
		var err error
		if data, _, err = readBinaryProp(item, prop); err != nil {
			return nil, err
		}
	}
	dec, err := decodeImageBytes(data)
	if err != nil {
		return nil, err
	}
	return dec.Img, nil
}

// outputMeta names the edited file: file_name when set, else the input name
// with its extension changed if the format changed, else image.<ext>.
func outputMeta(s editSettings, in fileMeta, ext string) fileMeta {
	m := fileMeta{FileName: in.FileName, MimeType: mimeForExt(ext), Extension: ext}
	inExt := normalizeExt(strings.TrimPrefix(filepath.Ext(in.FileName), "."))
	if in.FileName != "" && inExt == ext {
		m.Extension = strings.TrimPrefix(filepath.Ext(in.FileName), ".")
	} else if in.FileName != "" {
		m.FileName = swapExt(in.FileName, ext)
	} else {
		m.FileName = "image." + ext
	}
	if s.FileName != "" {
		m.FileName = s.FileName
	}
	return m
}

func normalizeExt(e string) string {
	switch e = strings.ToLower(e); e {
	case "jpg":
		return "jpeg"
	case "tif":
		return "tiff"
	}
	return e
}

// writeEditedFile writes the result in path mode: output_dir (or the input
// file's directory) / file_name, or <input>_edited_<ms>.<ext>.
func writeEditedFile(ctx context.Context, s editSettings, inPath, ext string, data []byte) (string, error) {
	if inPath == "" && s.OutputDir == "" {
		return "", fmt.Errorf("output_dir: required when create writes a file")
	}
	var out string
	var err error
	if s.FileName != "" {
		dir := filepath.Dir(inPath)
		if s.OutputDir != "" {
			dir = expandHome(s.OutputDir)
		}
		out, err = fsconfine.WritePath(ctx, filepath.Join(dir, filepath.Base(s.FileName)))
	} else {
		base := inPath
		if base == "" {
			base = filepath.Join(expandHome(s.OutputDir), fmt.Sprintf("image_%d.%s", time.Now().UnixNano()/1e6, ext))
		}
		out, err = buildOutputPath(ctx, base, s.OutputDir, "edited", ext)
	}
	if err != nil {
		return "", err
	}
	if s.OutputDir != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return "", fmt.Errorf("output_dir: %w", err)
		}
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return "", fmt.Errorf("write %q: %w", out, err)
	}
	return out, nil
}

// imageInfo is the information operation's output, merged into the item.
func imageInfo(d *decodedImage, size int) map[string]interface{} {
	b := d.Img.Bounds()
	w, h := b.Dx(), b.Dy()
	hasAlpha := false
	for i := 3; i < len(d.Img.Pix); i += 4 {
		if d.Img.Pix[i] != 255 {
			hasAlpha = true
			break
		}
	}
	info := map[string]interface{}{
		"format":       strings.ToUpper(d.Format),
		"mime_type":    mimeForExt(d.Format),
		"width":        w,
		"height":       h,
		"size":         map[string]interface{}{"width": w, "height": h},
		"geometry":     fmt.Sprintf("%dx%d", w, h),
		"aspect_ratio": math.Round(float64(w)/float64(h)*10000) / 10000,
		"color_model":  d.ColorModel,
		"depth":        d.Depth,
		"has_alpha":    hasAlpha,
		"file_size":    size,
	}
	if d.Orientation > 0 {
		info["orientation"] = d.Orientation
	}
	if d.PaletteSize > 0 {
		info["palette_colors"] = d.PaletteSize
	}
	return info
}
