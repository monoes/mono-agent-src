package image

// RemoveBackgroundNode removes the background from an image using the U2-Net
// ONNX model — the same model used by the Python rembg library.
//
// The actual inference implementation is compiled only when CGo is available
// (CGO_ENABLED=1). When CGo is disabled the node returns a clear error
// directing the user to rebuild with CGo.
//
// Type: "image.remove_background"
type RemoveBackgroundNode struct{}

func (n *RemoveBackgroundNode) Type() string { return "image.remove_background" }

// PublishesToThirdParty: image.remove_background sends nothing off this machine (issue #1418).
func (RemoveBackgroundNode) PublishesToThirdParty() bool { return false }
