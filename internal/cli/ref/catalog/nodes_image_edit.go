package catalog

// Reference entry for image.edit (the n8n Edit Image counterpart). catalog.go
// appends it to NodeDocs.

var imageEditNodeDocs = []nodeDoc{
	{
		Type:     "image.edit",
		Category: "image",
		Short:    "Edit images: resize, crop, rotate, draw, text, composite, blur and more (n8n Edit Image)",
		Description: `Edits images in pure Go, no ImageMagick needed. One operation per node, or
several in order with operation=multi_step:

  blur         Gaussian blur (sigma)
  border       add border_width / border_height pixels in border_color
  composite    lay the image in data_property_name_composite over this one at
               position_x/position_y with an operator (Over, In, Out, Atop, Xor,
               Plus, Add, Minus, Subtract, Multiply, Difference, Divide,
               Bumpmap, Copy, CopyOpacity, CopyRed/Green/Blue)
  create       new width x height canvas in background_color (no input needed)
  crop         width x height region at position_x/position_y
  draw         filled rectangle (corner_radius), filled circle or 1px line
  information  add format, width, height, size, geometry, color_model, depth,
               has_alpha, file_size, orientation (JPEG EXIF) to the item
  resize       resize_option maximumArea (area width*height), minimumArea
               (cover), ignoreAspectRatio, onlyIfLarger, onlyIfSmaller, percent
  rotate       any angle clockwise; exposed corners get background_color
  shear        degrees_x / degrees_y; exposed areas get background_color
  text         write text (font_size, font_color, alignment, line_length wrap)
               in the built-in Go font or a TTF/OTF given in font
  transparent  make color (within fuzz percent) fully transparent

Input/output: by default the image is a binary field — an item JSON field
holding base64 (data_property_name, default "data") with file_name, mime_type,
file_extension and file_size next to it, the shape http.request and the
data.* nodes use. The result replaces it (or goes to destination_key).
Set "field" to work on a file path instead: the result is written to a new
file (output_dir, default the source's directory) and its path stored in
output_field, like image.resize. Items that carry only image_path / path /
file_path / media_path are read from that path automatically.`,
		Config: `{
  "operation": "multi_step",
  "data_property_name": "data",
  "operations": [
    { "operation": "resize", "width": 1200, "height": 1200, "resize_option": "onlyIfLarger" },
    { "operation": "border", "border_width": 20, "border_height": 20, "border_color": "#ffffff" },
    { "operation": "text", "text": "Summer sale", "font_size": 48,
      "font_color": "#ffffff", "vertical_alignment": "south", "position_y": 40 }
  ],
  "format": "jpeg",       // bmp | gif | jpeg | png | tiff | webp (written as png)
  "quality": 90,          // jpeg only
  "file_name": "",        // output file name; default keeps the input name
  "destination_key": ""   // output binary field; default = data_property_name
}`,
		Inputs:  "items carrying an image as a binary field (base64) or a file path; none needed for create",
		Outputs: "the same items with the edited image (or, for information, the image details merged into the item)",
		Notes: `Colors: #rgb, #rrggbb, #rrggbbaa (last byte = alpha, ff opaque), rgb()/rgba() or a
name. n8n workflows use GraphicsMagick's reading of #rrggbbaa (last byte =
opacity, 00 opaque): set "hex_alpha": "opacity" for those.
Images over 100 megapixels (input or result) and files over 256 MiB are refused.
JPEG EXIF orientation is applied on read. multi_step also accepts n8n's
camelCase step keys (borderWidth, resizeOption, ...).`,
	},
}
