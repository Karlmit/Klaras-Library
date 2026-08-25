// Package covers generates and serves book cover images.
//
// calibre-web resizes covers on every request, which is one of the measured
// causes of its slowness at scale (~10s of a sync request spent on covers).
// Here every size is generated once by a background worker, written to a cache
// directory, and afterwards served as a static file with a long-lived
// validator.
package covers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"

	"github.com/Karlmit/Klaras-Library/internal/jobs"
)

// Size is a pre-generated thumbnail size.
type Size struct {
	Name  string
	Width int
}

// Sizes are the only widths ever produced. Keeping the set small and fixed is
// what makes the cache bounded and the request path a plain file read; an
// arbitrary-size endpoint would put us back to resizing per request.
var Sizes = []Size{
	{Name: "grid", Width: 200},   // cover grid, 2x for retina at ~100 CSS px
	{Name: "detail", Width: 400}, // book detail panel
	{Name: "kobo", Width: 600},   // what the device asks for
}

// SizeByName looks up a configured size.
func SizeByName(n string) (Size, bool) {
	for _, s := range Sizes {
		if s.Name == n {
			return s, true
		}
	}
	return Size{}, false
}

// ErrNoCover means the book has no cover file on disk.
var ErrNoCover = errors.New("no cover")

// Service generates and locates cover images.
type Service struct {
	libraryRoot string
	cacheDir    string
}

// New builds a cover service.
func New(libraryRoot, cacheDir string) *Service {
	return &Service{libraryRoot: libraryRoot, cacheDir: filepath.Join(cacheDir, "covers")}
}

// SourcePath is where Calibre keeps the full-size cover for a book.
func (s *Service) SourcePath(bookPath string) string {
	return filepath.Join(s.libraryRoot, bookPath, "cover.jpg")
}

// thumbPath spreads files over 256 subdirectories.
//
// A single directory holding 28,038 x 3 files is slow to list and unpleasant to
// inspect; hashing the key into a two-level fan-out keeps each directory small.
func (s *Service) thumbPath(uuid, size string) string {
	sum := sha1.Sum([]byte(uuid))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(s.cacheDir, h[:2], fmt.Sprintf("%s-%s.jpg", h, size))
}

// ThumbPath returns the cached path for a size, and whether it exists.
func (s *Service) ThumbPath(uuid, size string) (string, bool) {
	p := s.thumbPath(uuid, size)
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p, true
	}
	return p, false
}

// Generate renders every configured size for one book.
func (s *Service) Generate(bookPath, uuid string) error {
	src := s.SourcePath(bookPath)
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNoCover, src)
		}
		return err
	}
	defer f.Close()

	img, err := imaging.Decode(f, imaging.AutoOrientation(true))
	if err != nil {
		return fmt.Errorf("decode %s: %w", src, err)
	}

	for _, size := range Sizes {
		if err := s.writeThumb(img, uuid, size); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) writeThumb(img image.Image, uuid string, size Size) error {
	out := s.thumbPath(uuid, size.Name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}

	// Never upscale: a small source stays small rather than being blurred up.
	w := size.Width
	if b := img.Bounds(); b.Dx() < w {
		w = b.Dx()
	}
	// Height 0 preserves the aspect ratio; book covers are not all 2:3 and
	// cropping to a fixed ratio would cut off titles.
	resized := imaging.Resize(img, w, 0, imaging.Lanczos)

	// Write to a temporary file and rename, so a reader never sees a
	// half-written JPEG and a crash cannot leave a truncated cache entry.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := imaging.Encode(tmp, resized, imaging.JPEG, imaging.JPEGQuality(82)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), out)
}

// ThumbnailPayload is the job payload for cover generation.
type ThumbnailPayload struct {
	BookID int64  `json:"book_id"`
	UUID   string `json:"uuid"`
	Path   string `json:"path"`
}

// Handler processes cover generation jobs.
func (s *Service) Handler() jobs.Handler {
	return func(ctx context.Context, j *jobs.Job) error {
		var p ThumbnailPayload
		if err := j.Decode(&p); err != nil {
			return fmt.Errorf("%w: bad payload: %v", jobs.ErrPermanent, err)
		}
		if err := s.Generate(p.Path, p.UUID); err != nil {
			// A missing or corrupt source will not fix itself on retry.
			if errors.Is(err, ErrNoCover) || strings.Contains(err.Error(), "decode") {
				return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
			}
			return err
		}
		return nil
	}
}

// EnqueueMissing queues cover generation for books whose thumbnails are absent.
// Returns how many were queued.
func (s *Service) EnqueueMissing(ctx context.Context, q *jobs.Queue, rows []ThumbnailPayload) (int, error) {
	n := 0
	for _, p := range rows {
		if _, ok := s.ThumbPath(p.UUID, Sizes[0].Name); ok {
			continue
		}
		if err := q.Enqueue(ctx, jobs.KindThumbnail, p.UUID, p, 200); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Placeholder writes a neutral cover for books that have none, so the grid
// never shows a broken image.
func Placeholder(w io.Writer, width int) error {
	if width < 1 {
		width = 200
	}
	h := width * 3 / 2
	// --v-100 from the design tokens, so the gap reads as part of the design
	// rather than as a failure.
	img := imaging.New(width, h, colorV100)
	return imaging.Encode(w, img, imaging.JPEG, imaging.JPEGQuality(70))
}

// colorV100 is --v-100 from web/src/styles/tokens.css (#E9DCF7).
var colorV100 = color.NRGBA{R: 0xE9, G: 0xDC, B: 0xF7, A: 0xFF}
