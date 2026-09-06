package contracts

import (
	"net/url"
)

func ValidateAccessRefresh(original *CreateRunRequest, refresh *AccessRefreshTargets) *validationError {
	if refresh == nil {
		return nil
	}

	seen := map[string]bool{}
	for _, f := range refresh.Files {
		if f.ID == "" {
			return validationErrorf(ErrInvalidAccessRefresh, "files: refreshed entry has empty id")
		}
		if seen[f.ID] {
			return validationErrorf(ErrInvalidAccessRefresh, "files: duplicate refreshed id %q", f.ID)
		}
		seen[f.ID] = true
		orig, ok := findResource(original.Files, f.ID)
		if !ok {
			return validationErrorf(ErrInvalidAccessRefresh, "files: id %q is not a frozen file", f.ID)
		}
		if verr := sameOriginPath(orig.DownloadURL, f.DownloadURL, "files["+f.ID+"]"); verr != nil {
			return verr
		}
	}

	seen = map[string]bool{}
	for _, sk := range refresh.Skills {
		if sk.ID == "" {
			return validationErrorf(ErrInvalidAccessRefresh, "skills: refreshed entry has empty id")
		}
		if seen[sk.ID] {
			return validationErrorf(ErrInvalidAccessRefresh, "skills: duplicate refreshed id %q", sk.ID)
		}
		seen[sk.ID] = true
		orig, ok := findResource(original.Skills, sk.ID)
		if !ok {
			return validationErrorf(ErrInvalidAccessRefresh, "skills: id %q is not a frozen skill", sk.ID)
		}
		if verr := sameOriginPath(orig.DownloadURL, sk.DownloadURL, "skills["+sk.ID+"]"); verr != nil {
			return verr
		}
	}

	if refresh.ResultBundle != nil {
		if original.ResultBundle == nil {
			return validationErrorf(ErrInvalidAccessRefresh, "result_bundle: the run froze no result bundle; a refresh cannot add one")
		}
		if verr := sameOriginPath(original.ResultBundle.UploadURL, refresh.ResultBundle.UploadURL, "result_bundle"); verr != nil {
			return verr
		}
	}

	return nil
}

func findResource(res []ResourceRef, id string) (ResourceRef, bool) {
	for _, r := range res {
		if r.ID == id {
			return r, true
		}
	}
	return ResourceRef{}, false
}

func sameOriginPath(frozen, refreshed, label string) *validationError {
	if refreshed == "" {
		return validationErrorf(ErrInvalidAccessRefresh, "%s: refreshed url is empty", label)
	}
	fu, err := url.Parse(frozen)
	if err != nil {
		return validationErrorf(ErrInvalidAccessRefresh, "%s: frozen url is invalid", label)
	}
	ru, err := url.Parse(refreshed)
	if err != nil {
		return validationErrorf(ErrInvalidAccessRefresh, "%s: refreshed url is invalid", label)
	}
	if ru.Scheme != fu.Scheme || ru.Host != fu.Host || ru.EscapedPath() != fu.EscapedPath() || ru.User != nil || ru.Fragment != "" {
		return validationErrorf(ErrInvalidAccessRefresh, "%s: refreshed url changes scheme/host/port/path", label)
	}
	return nil
}
