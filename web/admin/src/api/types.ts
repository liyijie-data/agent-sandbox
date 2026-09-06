export interface AdminClientView {
  client_id: string;
  name: string;
  concurrency_quota: number;
  created_at: string;
}

export interface AdminAPIKeyView {
  id: string;
  label: string;
  active: boolean;
  created_at: string;
}

export interface AdminClientDetailResponse {
  client_id: string;
  name: string;
  concurrency_quota: number;
  created_at: string;
  api_keys: AdminAPIKeyView[];
}

export interface AdminImageRow {
  registration_id: string;
  image_id: string;
  digest: string;
  repository: string;
  status: string;
  warm_pool_replicas: number;
  validation_ref: string;
  created_at: string;
}

export interface AdminRunRow {
  run_id: string;
  req_id: string;
  status: string;
  current_stage: number;
  created_at: string;
  terminal_at: string | null;
}

export interface AdminPlatformNetworkResponse {
  active_revision: number | null;
  active_revision_id: string | null;
  desired_revision_id: string | null;
  rollout_status: string;
  error_summary: string;
  updated_at: string | null;
}

export interface AdminPlatformWarmPoolResponse {
  budget: number;
  max_per_image: number;
  default_pool: number;
  configured: boolean;
}

export interface CreateClientResponse {
  client_id: string;
  name: string;
  api_key: string;
  api_key_hash: string;
}

export interface RegistrationView {
  registration_id: string;
  client_id: string;
  image_id: string;
  digest: string;
  repository: string;
  status: string;
  phase: string;
  failure_codes: string[];
  validation_ref: string;
}

export class PlatformError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}
