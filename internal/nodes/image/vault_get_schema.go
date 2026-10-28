package image

// ImageVaultGetNodeSchema documents the config keys ImageVaultGetNode.Execute
// reads out of its map[string]interface{} config — see
// internal/tools/schemagen's doc comment for the tag grammar.
type ImageVaultGetNodeSchema struct {
	Field string `json:"field" schema:"label=Vault Id Field,type=text,default=vault_id,help=Item field holding the media vault id (e.g. img-001， with or without the leading @)."`

	OutputField string `json:"output_field" schema:"label=Output Field,type=text,default=image_path,help=Item field where the resolved local file path is written."`
}
