// Package imagescan finds media files (images, audio, video) under a profile
// folder. Pure filesystem — no DB/vault dependency, mirroring internal/docscan.
package imagescan

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// AllowedExtensions are the (lowercase, no dot) image extensions treated
// as images for discovery in the profile folder.
var AllowedExtensions = map[string]bool{
	"png":  true,
	"jpg":  true,
	"jpeg": true,
	"gif":  true,
	"webp": true,
	"svg":  true,
	"bmp":  true,
	"ico":  true,
	"tiff": true,
	"avif": true,
}

// AudioExtensions and VideoExtensions are the (lowercase, no dot) audio and
// video extensions the media vault tracks beside AllowedExtensions' images.
var AudioExtensions = map[string]bool{
	"mp3":  true,
	"wav":  true,
	"m4a":  true,
	"aac":  true,
	"ogg":  true,
	"oga":  true,
	"opus": true,
	"flac": true,
}

var VideoExtensions = map[string]bool{
	"mp4":  true,
	"webm": true,
	"mov":  true,
	"m4v":  true,
	"mkv":  true,
	"ogv":  true,
}

// Media kinds MediaKind returns.
const (
	KindImage = "image"
	KindAudio = "audio"
	KindVideo = "video"
)

// MediaKind returns KindImage, KindAudio or KindVideo for filename's
// extension (case-insensitive), or "" for anything else.
func MediaKind(filename string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	switch {
	case AllowedExtensions[ext]:
		return KindImage
	case AudioExtensions[ext]:
		return KindAudio
	case VideoExtensions[ext]:
		return KindVideo
	}
	return ""
}

// IsMediaFile reports whether filename is an image, audio or video file.
func IsMediaFile(filename string) bool { return MediaKind(filename) != "" }

// isDotDir reports whether name is a dot-prefixed directory (.monomind,
// .git, or any other dot-folder) — never user project content to discover.
func isDotDir(name string) bool {
	return strings.HasPrefix(name, ".")
}

// isSkipDir reports whether a directory should be skipped entirely.
// In addition to dot-directories, node_modules is skipped to avoid
// scanning thousands of external dependency assets.
func isSkipDir(name string) bool {
	return isDotDir(name) || name == "node_modules"
}

// IsImageFile reports whether filename's extension is in AllowedExtensions
// (case-insensitive, dot-insensitive).
func IsImageFile(filename string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ext == "" {
		return false
	}
	return AllowedExtensions[ext]
}

// InScannedTree reports whether path lies under root somewhere Scan(root)
// walks: not root itself and not inside a dot-directory or node_modules.
// Both are compared as given (after cleaning), so pass them in the same
// form Scan would see them.
func InScannedTree(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	dirs := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	for _, d := range dirs {
		if d != "." && isSkipDir(d) {
			return false
		}
	}
	return true
}

// FileInfo is one matched file from a scan.
type FileInfo struct {
	Path      string // absolute or root-relative path
	Filename  string
	SizeBytes int64
	ModTime   int64 // UnixNano
}

// Scan walks root recursively and returns every regular file matching
// IsImageFile -- what `media sync` tracks unless the profile has switched
// audio and video discovery on (ScanMedia), skipping any dot-prefixed directory (.monomind, .git,
// etc.) or node_modules other than root itself.
//
// A missing root (profile folder not yet created) returns (nil, nil), not
// an error — mirroring docscan.Scan. Symlinked subdirectories are not followed
// (fs.WalkDir's default), avoiding cyclic-symlink loops.
func Scan(root string) ([]FileInfo, error) { return scan(root, IsImageFile) }

// ScanMedia is Scan for every image, audio and video file.
func ScanMedia(root string) ([]FileInfo, error) { return scan(root, IsMediaFile) }

func scan(root string, match func(string) bool) ([]FileInfo, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}

	var found []FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // transient read error on one entry -- skip it, keep walking
		}
		if d.IsDir() {
			if path != root && isSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !match(d.Name()) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		found = append(found, FileInfo{
			Path:      path,
			Filename:  d.Name(),
			SizeBytes: info.Size(),
			ModTime:   info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
