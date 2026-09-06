package contracts

import "strings"

func ValidateCapabilityAdmission(req *CreateRunRequest, declared []string) *validationError {
	if req == nil {
		return validationErrorf(ErrInvalidRequest, "nil create run request")
	}
	has := make(map[string]bool, len(declared))
	for _, c := range declared {
		has[c] = true
	}

	missing := []string{}
	if len(req.Files) > 0 && !has[CapabilityFiles] {
		missing = append(missing, CapabilityFiles)
	}
	if len(req.Skills) > 0 && !has[CapabilitySkills] {
		missing = append(missing, CapabilitySkills)
	}
	if req.ResultBundle != nil && !has[CapabilityArtifacts] {
		missing = append(missing, CapabilityArtifacts)
	}
	openAPI := false
	mcp := false
	for _, t := range req.Tools {
		switch t.Type {
		case ToolTypeOpenAPI:
			openAPI = true
		case ToolTypeMCP:
			mcp = true
		}
	}
	if openAPI && !has[CapabilityOpenAPI] {
		missing = append(missing, CapabilityOpenAPI)
	}
	if mcp && !has[CapabilityMCP] {
		missing = append(missing, CapabilityMCP)
	}
	if len(missing) > 0 {
		return validationErrorf(ErrCapabilityUnsupported,
			"image does not declare required capability: %s", strings.Join(missing, ","))
	}
	return nil
}
