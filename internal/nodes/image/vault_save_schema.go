package image

// ImageVaultSaveNodeSchema documents the config keys ImageVaultSaveNode.Execute
// reads out of its map[string]interface{} config — see
// internal/tools/schemagen's doc comment for the tag grammar.
type ImageVaultSaveNodeSchema struct {
	Field string `json:"field" schema:"label=Image Path Field,type=text,help=Item field holding the local image， audio or video path to save. Defaults to image_path， path， file_path， or media_path (first non-empty)."`

	Source string `json:"source" schema:"label=Source Tag,type=text,default=workflow,help=Free-text tag stored alongside the vault entry (e.g. gemini， upload， workflow)."`

	OutputField string `json:"output_field" schema:"label=Output Field,type=text,default=vault_id,help=Item field where the new media vault id (e.g. img-001) is written."`
}
