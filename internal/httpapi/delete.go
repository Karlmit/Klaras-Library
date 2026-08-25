package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Karlmit/Klaras-Library/internal/library"
)

// handleDeleteBook removes a book and, optionally, its files.
//
// Deleting the row alone would leave orphaned files on disk that the watch
// folder might re-import, so removing the files is the default. Keeping them
// has to be asked for explicitly.
func (s *Server) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id, err := intParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad book id")
		return
	}
	keepFiles := queryBool(r, "keep_files")

	info, err := s.lib.PathInfo(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "delete lookup")
		return
	}

	var removed int
	var dirRemoved bool
	if !keepFiles {
		names := make([]string, 0, len(info.Files))
		for _, f := range info.Files {
			names = append(names, f.Name)
		}
		res, err := s.files.DeleteBookFiles(r.Context(), id, info.Path, names)
		if err != nil {
			s.log.Error("could not delete book files", "book", id, "err", err)
			writeErr(w, http.StatusInternalServerError,
				"the book's files could not be removed, so nothing was deleted")
			return
		}
		removed, dirRemoved = res.FilesRemoved, res.DirRemoved
	}

	// The row goes last. If the file removal failed we have already bailed out,
	// so the catalogue never loses a book whose files are still on disk.
	if _, err := s.db.Pool.Exec(r.Context(), `DELETE FROM books WHERE id=$1`, id); err != nil {
		s.fail(w, r, err, "delete book")
		return
	}
	s.log.Info("book deleted", "book", id, "title", info.Title,
		"files_removed", removed, "by", s.currentUser(r).Username)

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted", "files_removed": removed, "directory_removed": dirRemoved,
	})
}

// handleBulkDelete removes many books at once.
func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs       []int64 `json:"ids"`
		KeepFiles bool    `json:"keep_files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if len(in.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "no books selected")
		return
	}
	if len(in.IDs) > 2000 {
		writeErr(w, http.StatusBadRequest, "too many books in one request (max 2000)")
		return
	}

	var deleted, failed, filesRemoved int
	for _, id := range in.IDs {
		info, err := s.lib.PathInfo(r.Context(), id)
		if err != nil {
			if err == library.ErrNotFound {
				continue // already gone
			}
			failed++
			continue
		}
		if !in.KeepFiles {
			names := make([]string, 0, len(info.Files))
			for _, f := range info.Files {
				names = append(names, f.Name)
			}
			res, err := s.files.DeleteBookFiles(r.Context(), id, info.Path, names)
			if err != nil {
				s.log.Error("bulk delete: files", "book", id, "err", err)
				failed++
				continue // leave the row, so the book is not lost from the catalogue
			}
			filesRemoved += res.FilesRemoved
		}
		if _, err := s.db.Pool.Exec(r.Context(), `DELETE FROM books WHERE id=$1`, id); err != nil {
			failed++
			continue
		}
		deleted++
	}
	s.log.Warn("bulk delete", "deleted", deleted, "failed", failed,
		"files_removed", filesRemoved, "by", s.currentUser(r).Username)

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted, "failed": failed, "files_removed": filesRemoved,
	})
}
