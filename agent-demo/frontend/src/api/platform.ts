import { request } from "./client";
import type {
  NetworkConfigRequest,
  NetworkConfigResponse,
} from "./types";

export function getNetworkConfig(): Promise<NetworkConfigResponse> {
  return request<NetworkConfigResponse>("/api/network/config");
}

export function putNetworkConfig(
  payload: NetworkConfigRequest,
): Promise<NetworkConfigResponse> {
  return request<NetworkConfigResponse>("/api/network/config", {
    method: "PUT",
    body: JSON.stringify(payload),
  });
}

export function getImageNetworkConfig(
  imageId: string,
): Promise<NetworkConfigResponse> {
  return request<NetworkConfigResponse>(`/api/network/config/${encodeURIComponent(imageId)}`);
}

export function putImageNetworkConfig(
  imageId: string,
  payload: NetworkConfigRequest,
): Promise<NetworkConfigResponse> {
  return request<NetworkConfigResponse>(`/api/network/config/${encodeURIComponent(imageId)}`, {
    method: "PUT",
    body: JSON.stringify(payload),
  });
}