package httpapi

import (
	"errors"
	"net/http"

	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Server) memberRepository(w http.ResponseWriter) (store.MemberRepository, bool) {
	repo, ok := s.Store.(store.MemberRepository)
	if !ok {
		Error(w, 501, "members_unavailable", "Member management is unavailable")
	}
	return repo, ok
}
func memberError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		Error(w, 409, "last_admin", "At least one enabled administrator is required")
	case errors.Is(err, store.ErrMemberInvalid):
		Error(w, 422, "invalid_member", err.Error())
	case errors.Is(err, store.ErrMemberPreviewStale):
		Error(w, 409, "import_preview_stale", "Directory or input changed; preview the import again")
	case errors.Is(err, store.ErrMemberImportConflict):
		Error(w, 409, "import_conflict", "Resolve the directory conflicts before importing")
	default:
		fail(w, err)
	}
}
func (s *Server) GetMember(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	member, err := repo.Member(r.Context(), r.PathValue("id"))
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 200, map[string]any{"user": member})
}
func (s *Server) DeleteMember(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	if err := repo.DeleteMember(r.Context(), r.PathValue("id")); err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 204, nil)
}
func (s *Server) PreviewMemberImport(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	var input store.MemberImportRequest
	if !Decode(w, r, &input) {
		return
	}
	preview, err := repo.PreviewMemberImport(r.Context(), input)
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 200, preview)
}
func (s *Server) ImportMembers(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	var input store.MemberImportRequest
	if !Decode(w, r, &input) {
		return
	}
	result, err := repo.ImportMembers(r.Context(), input)
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 200, result)
}
