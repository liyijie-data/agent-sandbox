package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"agent-platform/internal/contracts"
)

func (e *Executor) readResult(ctx context.Context, pod Pod, exitCode int) (*contracts.RuntimeResult, string, error) {
	data, err := pod.Read(ctx, ResultPath)
	if err != nil {
		return nil, "", fmt.Errorf("executor: read result.json: %w", err)
	}
	var res contracts.RuntimeResult
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		return nil, "", fmt.Errorf("executor: decode result.json: %w", err)
	}
	switch res.Status {
	case contracts.RuntimeOk, contracts.RuntimeAwaitingInput:
		if exitCode != 0 {
			return &res, string(contracts.ErrRuntimeProtocolInvalid), nil
		}
	case contracts.RuntimeError:
		if exitCode == 0 {
			return &res, string(contracts.ErrRuntimeProtocolInvalid), nil
		}
	default:
		return &res, string(contracts.ErrRuntimeProtocolInvalid), nil
	}
	return &res, "", nil
}
