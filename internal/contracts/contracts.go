package contracts

const (
	PublicAPIVersionPrefix = "/api/v1"

	RuntimeContractVersion = "agent-platform-runtime/v1"
)

const (
	CapabilityCoreExec        = "execution"
	CapabilityCoreEvents      = "events"
	CapabilityCoreCancel      = "cancel"
	CapabilityCorePauseResume = "pause_resume"
	CapabilityCoreSteering    = "steering"
)

const (
	CapabilityFiles     = "files"
	CapabilitySkills    = "skills"
	CapabilityOpenAPI   = "openapi_tools"
	CapabilityMCP       = "mcp_tools"
	CapabilityArtifacts = "artifacts"
)

var (
	LegacyCoreCapabilities = []string{
		CapabilityCoreExec, CapabilityCoreEvents, CapabilityCoreCancel,
		CapabilityCorePauseResume, CapabilityCoreSteering,
	}
	CoreCapabilities = []string{
		CapabilityCoreExec, CapabilityCoreEvents, CapabilityCoreCancel,
		CapabilityCorePauseResume, CapabilityCoreSteering,
		CapabilityFiles, CapabilitySkills, CapabilityOpenAPI,
		CapabilityMCP, CapabilityArtifacts,
	}
	ExtensionCapabilities = []string{CapabilityFiles, CapabilitySkills, CapabilityOpenAPI, CapabilityMCP, CapabilityArtifacts}
	KnownCapabilities     = func() map[string]bool {
		m := make(map[string]bool, len(CoreCapabilities)+len(ExtensionCapabilities))
		for _, c := range CoreCapabilities {
			m[c] = true
		}
		for _, c := range ExtensionCapabilities {
			m[c] = true
		}
		return m
	}()
)

const (
	SteerIDMaxLen             = 128
	SteerContentMaxBytes      = 16 * 1024
	SteerRequestMaxBytes      = 128 * 1024
	SteerMaxPerRun            = 128
	SteerContentTotalMaxBytes = 1 * 1024 * 1024
)

const CreateRunMaxBytes = 2 * 1024 * 1024
