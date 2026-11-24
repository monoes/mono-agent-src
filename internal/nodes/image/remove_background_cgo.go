//go:build cgo

package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ── constants ──────────────────────────────────────────────────────────────────

const (
	// u2net input/output dimensions
	u2netSize = 320

	// ORT version bundled with yalue/onnxruntime_go v1.27.0 (API version 24)
	ortVersion = "1.24.1"

	// Model download URLs — same files rembg uses
	u2netModelURL = "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2net.onnx"

	// u2net.onnx is exactly this long (the size of the release asset); a file of any other
	// length is a truncated or substituted download.
	u2netModelSize = 175997641

	// Upper bound for the ONNX Runtime archive (the real ones are about 8-15 MB).
	ortArchiveMaxBytes = 200 << 20

	// Upper bound for the shared library extracted from it.
	ortLibMaxBytes = 500 << 20

	// ImageNet normalization (rembg base.py)
	meanR, meanG, meanB = 0.485, 0.456, 0.406
	stdR, stdG, stdB    = 0.229, 0.224, 0.225
)

// ortLibURLs returns the download URL for the ORT shared library for the current platform.
// Archive format: tgz containing lib/libonnxruntime.so.x.y.z (Linux) or
// lib/libonnxruntime.x.y.z.dylib (macOS).
func ortLibURL() (archiveURL, libGlob string, err error) {
	base := "https://github.com/microsoft/onnxruntime/releases/download/v" + ortVersion
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return base + "/onnxruntime-osx-arm64-" + ortVersion + ".tgz",
				"libonnxruntime*.dylib", nil
		}
		return base + "/onnxruntime-osx-x86_64-" + ortVersion + ".tgz",
			"libonnxruntime*.dylib", nil
	case "linux":
		return base + "/onnxruntime-linux-x64-" + ortVersion + ".tgz",
			"libonnxruntime.so*", nil
	default:
		return "", "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

// Pinned SHA-256 digests (lower-case hex) of the downloads. An empty entry is not verified by
// hash (the size checks above still apply).
//
// u2netModelSHA256: computed 2026-11-24 with sha256sum on a fresh curl download of u2netModelURL
// (175997641 bytes, equal to u2netModelSize and to the size of the release asset in the release
// API). Second source: the file's md5 (60024c5c889badc19c04ad937298a77b) equals the md5 that
// rembg's own sessions/u2net.py pins for this URL (rembg v2.0.57). The release publishes no
// SHA-256 digest of its own.
const u2netModelSHA256 = "8d10d2f3bb75ae3b6d527c77944fc5e7dcd94b29809d47a739a7a728a912b491"

// ortArchiveSHA256 is keyed "linux/amd64", "darwin/arm64", "darwin/amd64" (what ortLibURL
// selects; linux on any other arch is served the x64 archive and so has no entry). All three
// are STILL EMPTY: on 2026-11-24 every ONNX Runtime v1.24.1 archive URL answered HTTP 404 and
// the microsoft/onnxruntime repository and release API were not reachable, so no digest could be
// computed or cross-checked. Do not fill them from memory; download each URL, run sha256sum and
// compare with the release's published checksum first.
var ortArchiveSHA256 = map[string]string{}

// ── ORT & model singletons ─────────────────────────────────────────────────────

var (
	ortInitOnce sync.Once
	ortInitErr  error

	u2netSession  *ort.AdvancedSession
	sessionMu     sync.Mutex
	sessionInput  *ort.Tensor[float32]
	sessionOutput *ort.Tensor[float32]
)

// modelsDir returns ~/.monoagent/models
func modelsDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".monoagent", "models")
	}
	return filepath.Join(os.TempDir(), "monoagent", "models")
}

// libDir returns ~/.monoagent/lib
func libDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".monoagent", "lib")
	}
	return filepath.Join(os.TempDir(), "monoagent", "lib")
}

// ensureORT downloads the ONNX Runtime shared library if not present and
// sets the shared library path for yalue/onnxruntime_go.
func ensureORT() error {
	dir := libDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create lib dir: %w", err)
	}

	// Check if library already present
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && !strings.Contains(e.Name(), ".part-") && (strings.HasSuffix(e.Name(), ".dylib") || strings.HasSuffix(e.Name(), ".so") ||
			strings.Contains(e.Name(), "libonnxruntime")) {
			ort.SetSharedLibraryPath(filepath.Join(dir, e.Name()))
			return nil
		}
	}

	archURL, libGlob, err := ortLibURL()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[image.remove_background] Downloading ONNX Runtime %s...\n", ortVersion)
	tgzPath := filepath.Join(dir, "ort.tgz")
	if err := downloadFile(archURL, tgzPath, ortArchiveMaxBytes, 0, ortArchiveSHA256[runtime.GOOS+"/"+runtime.GOARCH]); err != nil {
		return fmt.Errorf("download ORT: %w", err)
	}
	defer os.Remove(tgzPath)

	libPath, err := extractGlobFromTGZ(tgzPath, libGlob, dir)
	if err != nil {
		return fmt.Errorf("extract ORT library: %w", err)
	}

	ort.SetSharedLibraryPath(libPath)
	return nil
}

// ensureModel downloads u2net.onnx if not present and returns its path.
func ensureModel() (string, error) {
	dir := modelsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create models dir: %w", err)
	}

	dst := filepath.Join(dir, "u2net.onnx")
	if fi, err := os.Stat(dst); err == nil {
		if fi.Size() == u2netModelSize {
			return dst, nil
		}
		os.Remove(dst) // truncated or substituted: fetch it again
	}

	fmt.Fprintf(os.Stderr, "[image.remove_background] Downloading u2net.onnx (~176 MB)...\n")
	if err := downloadFile(u2netModelURL, dst, u2netModelSize, u2netModelSize, u2netModelSHA256); err != nil {
		return "", fmt.Errorf("download u2net.onnx: %w", err)
	}
	return dst, nil
}

// initORT is called once per process to set up the ORT environment and create
// the shared session + tensors.
func initORT(modelPath string) error {
	ortInitOnce.Do(func() {
		if err := ensureORT(); err != nil {
			ortInitErr = fmt.Errorf("ORT setup: %w", err)
			return
		}
		if err := ort.InitializeEnvironment(); err != nil {
			ortInitErr = fmt.Errorf("ORT init: %w", err)
			return
		}

		// Pre-allocate input/output tensors (reused every call under sessionMu)
		inputShape := ort.NewShape(1, 3, u2netSize, u2netSize)
		inputData := make([]float32, 1*3*u2netSize*u2netSize)
		var err error
		sessionInput, err = ort.NewTensor(inputShape, inputData)
		if err != nil {
			ortInitErr = fmt.Errorf("create input tensor: %w", err)
			return
		}

		outputShape := ort.NewShape(1, 1, u2netSize, u2netSize)
		outputData := make([]float32, 1*1*u2netSize*u2netSize)
		sessionOutput, err = ort.NewTensor(outputShape, outputData)
		if err != nil {
			ortInitErr = fmt.Errorf("create output tensor: %w", err)
			return
		}

		// Tensor names from the u2net.onnx model (inspected via GetInputOutputInfo):
		//   input:  "input.1"  [1,3,320,320] float32
		//   output: "1959"     [1,1,320,320] float32  (first/finest sigmoid — same as rembg ort_outs[0])
		u2netSession, err = ort.NewAdvancedSession(
			modelPath,
			[]string{"input.1"},
			[]string{"1959"},
			[]ort.Value{sessionInput},
			[]ort.Value{sessionOutput},
			nil,
		)
		if err != nil {
			ortInitErr = fmt.Errorf("create ORT session: %w", err)
		}
	})
	return ortInitErr
}

// ── helpers ────────────────────────────────────────────────────────────────────

// downloadFile fetches url into dst. The bytes go to a temporary file beside dst and are renamed
// into place only once complete, so an interrupted download never leaves a truncated library or
// model under its final name. At most maxBytes are accepted; wantSize (when > 0) is the exact size
// and wantSHA256 (when not empty) the hex digest the file must have.
func downloadFile(url, dst string, maxBytes, wantSize int64, wantSHA256 string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("download of %s is larger than the %d byte limit", url, maxBytes)
	}
	if wantSize > 0 && n != wantSize {
		return fmt.Errorf("download of %s is %d bytes, expected %d", url, n, wantSize)
	}
	if wantSHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA256) {
			return fmt.Errorf("download of %s has SHA-256 %s, expected %s", url, got, wantSHA256)
		}
	}
	return os.Rename(tmp.Name(), dst)
}

// extractGlobFromTGZ extracts the first file matching globPat from a .tgz
// archive into destDir, returning the full path to the extracted file.
func extractGlobFromTGZ(tgzPath, globPat, destDir string) (string, error) {
	f, err := os.Open(tgzPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		matched, _ := filepath.Match(globPat, base)
		if !matched {
			continue
		}
		outPath := filepath.Join(destDir, base)
		out, err := os.CreateTemp(destDir, base+".part-*")
		if err != nil {
			return "", err
		}
		n, err := io.Copy(out, io.LimitReader(tr, ortLibMaxBytes+1))
		out.Close()
		if err == nil && n > ortLibMaxBytes {
			err = fmt.Errorf("%s in the archive is larger than %d bytes", base, int64(ortLibMaxBytes))
		}
		if err == nil {
			err = os.Rename(out.Name(), outPath)
		}
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("no file matching %q found in archive", globPat)
}

// ── node ───────────────────────────────────────────────────────────────────────

func (n *RemoveBackgroundNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	field, _ := config["field"].(string)
	outputField, _ := config["output_field"].(string)
	outputDir, _ := config["output_dir"].(string)
	bgColor, _ := config["bgcolor"].(string)

	if outputField == "" {
		outputField = "image_path"
	}
	if outputDir != "" {
		outputDir = expandHome(outputDir)
	}

	// Ensure ORT + model are ready (idempotent after first call).
	modelPath, err := ensureModel()
	if err != nil {
		return nil, fmt.Errorf("image.remove_background: %w", err)
	}
	if err := initORT(modelPath); err != nil {
		return nil, fmt.Errorf("image.remove_background: %w", err)
	}

	outItems := make([]workflow.Item, 0, len(input.Items))

	for _, item := range input.Items {
		newJSON := copyMap(item.JSON)

		srcPath, err := imagePath(ctx, item.JSON, field)
		if err != nil {
			return nil, fmt.Errorf("image.remove_background: %w", err)
		}
		if srcPath == "" {
			return nil, fmt.Errorf("image.remove_background: no image path in item (tried field=%q)", field)
		}

		result, err := removeBackground(ctx, srcPath, outputDir, bgColor)
		if err != nil {
			return nil, fmt.Errorf("image.remove_background: %w", err)
		}
		newJSON[outputField] = result

		outItems = append(outItems, workflow.Item{JSON: newJSON, Binary: item.Binary})
	}

	return []workflow.NodeOutput{{Handle: "main", Items: outItems}}, nil
}

// removeBackground runs the full pipeline for a single image and returns the
// output file path.
func removeBackground(ctx context.Context, srcPath, outputDir, bgColor string) (string, error) {
	// 1. Open source image
	f, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", srcPath, err)
	}
	origImg, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", srcPath, err)
	}

	bounds := origImg.Bounds()
	origW := bounds.Max.X - bounds.Min.X
	origH := bounds.Max.Y - bounds.Min.Y

	// 2. Preprocess
	sessionMu.Lock()
	defer sessionMu.Unlock()

	preprocess(origImg, sessionInput.GetData())

	// 3. Run inference
	if err := u2netSession.Run(); err != nil {
		return "", fmt.Errorf("ORT inference: %w", err)
	}

	// 4. Post-process mask
	maskFloats := postprocess(sessionOutput.GetData())

	// 5. Apply mask
	rgba := applyMask(origImg, maskFloats, origW, origH)

	// 6. Optional background colour flatten
	var finalImg image.Image = rgba
	if bgColor != "" {
		flattened, err := flattenOntoColor(rgba, bgColor)
		if err != nil {
			return "", fmt.Errorf("invalid bgColor: %w", err)
		}
		finalImg = flattened
	}

	// 7. Save
	outPath, err := buildOutputPath(ctx, srcPath, outputDir, "_nobg", ".png")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create output: %w", err)
	}
	defer out.Close()
	if err := png.Encode(out, finalImg); err != nil {
		return "", fmt.Errorf("encode PNG: %w", err)
	}
	return outPath, nil
}
