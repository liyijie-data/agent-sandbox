import type { Resource, ResourceKind, Tool } from "../../api/types";

export interface SettingsResourcesApi {
  tools: Tool[];
  readySkills: Resource[];
  readySpecs: Resource[];
  upload: (kind: ResourceKind, file: File) => Promise<Resource>;
  loadAll: () => Promise<void>;
}