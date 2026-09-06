package contracts

func StandardRuntimeManifest() *Manifest {
	return &Manifest{
		ContractVersion: RuntimeContractVersion,
		ImageVersion:    "1.0.0",
		Entrypoint:      []string{"/app/bin/runtime"},
		StateFormat:     "go-runtime/1",
		Capabilities: []string{
			CapabilityCoreExec, CapabilityCoreEvents, CapabilityCoreCancel,
			CapabilityCorePauseResume, CapabilityCoreSteering, CapabilityFiles,
			CapabilitySkills, CapabilityOpenAPI, CapabilityMCP, CapabilityArtifacts,
		},
	}
}

func PythonReferenceRuntimeManifest() *Manifest {
	m := StandardRuntimeManifest()
	m.Entrypoint = []string{"python", "-m", "agent_runtime"}
	m.StateFormat = "agent-runtime-reference/1"
	return m
}
