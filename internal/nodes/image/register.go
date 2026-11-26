package image

import "github.com/monoes/mono-agent/internal/workflow"

// RegisterAll registers all Tier-1 image processing nodes into the given registry.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register("image.info", func() workflow.NodeExecutor { return &ImageInfoNode{} })
	r.Register("image.resize", func() workflow.NodeExecutor { return &ImageResizeNode{} })
	r.Register("image.crop", func() workflow.NodeExecutor { return &ImageCropNode{} })
	r.Register("image.thumbnail", func() workflow.NodeExecutor { return &ImageThumbnailNode{} })
	r.Register("image.convert", func() workflow.NodeExecutor { return &ImageConvertNode{} })
	r.Register("image.adjust", func() workflow.NodeExecutor { return &ImageAdjustNode{} })
	// Removed: fails with the removal message (see removedNode).
	r.Register("image.remove_background", func() workflow.NodeExecutor { return &removedNode{typeName: "image.remove_background"} })
	r.Register("image.vault_save", func() workflow.NodeExecutor { return &ImageVaultSaveNode{} })
	r.Register("image.vault_get", func() workflow.NodeExecutor { return &ImageVaultGetNode{} })
	r.Register("image.edit", func() workflow.NodeExecutor { return &ImageEditNode{} })
}
