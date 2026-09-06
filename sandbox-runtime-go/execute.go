package runtime

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/api"
	"agent-platform/sandbox-runtime-go/app"
	"agent-platform/sandbox-runtime-go/artifacts"
	"agent-platform/sandbox-runtime-go/checkpoint"
	contextpkg "agent-platform/sandbox-runtime-go/context"
	"agent-platform/sandbox-runtime-go/contextstore"
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/events"
	"agent-platform/sandbox-runtime-go/model"
	"agent-platform/sandbox-runtime-go/plugin"
	"agent-platform/sandbox-runtime-go/steering"
	"agent-platform/sandbox-runtime-go/trace"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type executionError struct{ code, typ string }

type contextProcessorFunc func(context.Context, *engine.State) error

func (f contextProcessorFunc) Process(ctx context.Context, state *engine.State) error {
	return f(ctx, state)
}

func (e *executionError) Error() string { return e.code }

func gmToolsJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func ExecuteRun(ctx context.Context, cfg *contracts.RuntimeConfig, supplied *engine.Agent, output string) error {
	if cfg == nil || contracts.ValidateMessages(cfg.Messages) != nil {
		return fmt.Errorf("runtime_protocol_invalid")
	}
	if e := contracts.ValidateTools(cfg.Tools); e != nil {
		return fmt.Errorf("runtime_protocol_invalid")
	}
	root := "/app"
	if v := os.Getenv("AGENT_RUNTIME_ROOT"); v != "" {
		root = v
	}
	if output == "" {
		output = filepath.Join(root, "output", "result.json")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Limits.RemainingExecutionSeconds)*time.Second)
	defer cancel()
	selected := app.DefaultConfig()
	traceEnabled := true
	window, reserve := 262144, 4096
	if snap := os.Getenv("AGENT_RUNTIME_PLUGINS_SNAPSHOT"); snap != "" {
		b, e := base64.StdEncoding.DecodeString(snap)
		if e != nil {
			return fmt.Errorf("plugin config invalid")
		}
		pc, e := plugin.DecodeConfig(b)
		if e != nil {
			return e
		}
		if e := app.ValidateProfile(pc); e != nil {
			return e
		}
		selected = pc
		for _, ps := range pc.Plugins {
			if ps.Name == "context.standard" && len(ps.Config) > 0 {
				var x struct {
					ContextWindowTokens int `json:"context_window_tokens"`
					ReserveOutputTokens int `json:"reserve_output_tokens"`
				}
				d := json.NewDecoder(bytes.NewReader(ps.Config))
				d.DisallowUnknownFields()
				if e := d.Decode(&x); e != nil {
					return e
				}
				var z any
				if e := d.Decode(&z); e != io.EOF {
					return fmt.Errorf("context config trailing data")
				}
				if x.ContextWindowTokens > 0 {
					window = x.ContextWindowTokens
				}
				if x.ReserveOutputTokens > 0 {
					reserve = x.ReserveOutputTokens
				}
			}
			if ps.Name == "trace.standard" && len(ps.Config) > 0 {
				var x struct {
					Enabled *bool `json:"enabled"`
				}
				d := json.NewDecoder(bytes.NewReader(ps.Config))
				d.DisallowUnknownFields()
				if e := d.Decode(&x); e != nil {
					return e
				}
				var extra any
				if e := d.Decode(&extra); e != io.EOF {
					return fmt.Errorf("trace config trailing data")
				}
				if x.Enabled != nil {
					traceEnabled = *x.Enabled
				}
			}
		}
		known := map[string]bool{"model.gateway": true, "tools.standard": true, "resources.standard": true, "context.standard": true, "loop.standard": true, "checkpoint.standard": true, "artifacts.standard": true, "events.standard": true, "trace.standard": true}
		for _, p := range pc.Plugins {
			if !known[p.Name] {
				return fmt.Errorf("unknown plugin %q", p.Name)
			}
		}
	}

	if cfg.Model.ContextWindowTokens != nil {
		window = *cfg.Model.ContextWindowTokens
	}
	if cfg.Model.MaxOutputTokens != nil {
		reserve = *cfg.Model.MaxOutputTokens
	}
	if raw, ok := cfg.Model.Parameters["max_tokens"]; ok && cfg.Model.MaxOutputTokens == nil {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			reserve = v
		}
	}
	if raw, ok := cfg.Model.Parameters["max_completion_tokens"]; ok && cfg.Model.MaxOutputTokens == nil {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			reserve = v
		}
	}
	if window <= 0 || reserve <= 0 || window <= reserve {
		return resultError(output, 0, "runtime_protocol_invalid", "model_limits")
	}
	versions := map[string]string{}
	for _, p := range selected.Plugins {
		versions[p.Name] = "go-runtime/1"
	}
	expectedCursor := int64(0)
	if cfg.Steering != nil {
		expectedCursor = cfg.Steering.AfterSeq
	}
	if supplied == nil {
		return resultError(output, 0, "agent_execution_failed", "model")
	}
	var restored engine.State
	var baseline artifacts.Baseline
	var omittedDiagnostics []string
	if cfg.Resume != nil {
		cpState, e := restoreCheckpoint(ctx, cfg.Resume.CheckpointPath, root, cfg.RunID, cfg.Stage, cfg.Fence, versions, expectedCursor, cfg.Limits.CheckpointMaxBytes)
		if e != nil {
			fmt.Fprintln(os.Stderr, "runtime checkpoint restore:", e)
			return resultError(output, 0, "resume_failed", "checkpoint")
		}
		var saved struct {
			Engine   engine.State       `json:"engine"`
			Baseline artifacts.Baseline `json:"baseline"`
		}
		if json.Unmarshal(cpState.Data, &saved) != nil {
			return resultError(output, 0, "resume_failed", "state")
		}
		restored = saved.Engine
		baseline = saved.Baseline
		omittedDiagnostics = append(omittedDiagnostics, cpState.Manifest.DiagnosticsMissing...)
		if cfg.Steering != nil && restored.Cursor != cfg.Steering.AfterSeq {
			return resultError(output, restored.Cursor, "resume_failed", "cursor")
		}
	}
	processor := &contextpkg.Processor{}
	tr, _ := trace.New(root, runtimeTraceOptions(cfg, versions, traceEnabled))
	defer tr.Freeze()
	ev := events.New(cfg.Runtime.BaseURL, cfg.Runtime.Token, cfg.ExecutionID)
	ev.Sanitizer = tr.Sanitize
	defer ev.Close()
	comp, e := app.BuildComposition(ctx, app.Env{Root: root, Cfg: cfg, Model: supplied.Model, Specs: cfg.Tools, Config: selected, Processor: processor, Trace: app.StandardTraceService{Recorder: tr}, Events: ev})
	if e != nil {
		return resultError(output, 0, "agent_execution_failed", "composition")
	}
	defer comp.Close()
	var toolCatalog func() []api.ToolDefinition
	if comp.Tools != nil {
		catalogBytes := window / 4
		if catalogBytes < 1024 {
			catalogBytes = 1024
		}
		if catalogBytes > 32<<10 {
			catalogBytes = 32 << 10
		}
		toolCatalog, e = app.ConfigureToolCatalog(comp.Tools, catalogBytes)
		if e != nil {
			return resultError(output, 0, "agent_execution_failed", "tool_catalog")
		}
		if _, ok := supplied.Model.(*model.GatewayModel); ok {

		}
	}
	refs, e := comp.Resources.Prepare(ctx, cfg, cfg.Resume != nil)
	if e != nil {
		return resultError(output, 0, "agent_execution_failed", "resources")
	}
	if cfg.Resume == nil {
		baseline, _ = comp.Artifacts.Snapshot(filepath.Join(root, "workspace"))
	}
	sources := make([]map[string]string, 0, len(cfg.Files)+len(cfg.Skills))
	for _, r := range cfg.Files {
		sources = append(sources, map[string]string{"kind": "file", "name": r.Name, "sha256": r.SHA256})
	}
	for _, r := range cfg.Skills {
		sources = append(sources, map[string]string{"kind": "skill", "name": r.Name, "sha256": r.SHA256})
	}
	_ = comp.Trace.Record("context.injected", map[string]any{"messages": refs, "sources": sources})
	if len(omittedDiagnostics) > 0 {
		_ = comp.Trace.Record("checkpoint.diagnostics_missing", map[string]any{"missing": omittedDiagnostics})
	}
	a := supplied
	a.Model, a.Tools = comp.Model, comp.Tools
	comp.Loop.Observe(comp.Trace.Record)
	a.Events = multiSink{traceSink{tr}, comp.Events}
	var hook func(context.Context, *engine.State) error
	if gm, ok := supplied.Model.(*model.GatewayModel); ok {
		gm.MaxOutputTokens = reserve
		gm.Parameters = cloneModelParameters(cfg.Model.Parameters)
		gm.Tools = a.Tools.Definitions()
		gm.Tools = append(gm.Tools, api.ToolDefinition{Name: "agent_request_input", Description: "Request human input", Parameters: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["question","choice","approval"]},"prompt":{"type":"string"},"options":{"type":"array","items":{"type":"object","properties":{"value":{"type":"string"},"label":{"type":"string"}},"required":["value","label"]}}},"required":["kind","prompt"]}`)})
		gm.OnRequest = func(raw json.RawMessage) error { return comp.Trace.Record("model.request", raw) }
		gm.OnResponse = func(raw json.RawMessage) error { return comp.Trace.Record("model.response", raw) }
		gm.OnDelta = func(d api.ModelDelta) error {
			typ := "agent.token"
			if d.Kind == "reasoning" {
				typ = "agent.reasoning"
			}
			return ev.Emit(ctx, engine.Event{Type: typ, Data: map[string]string{"delta": d.Text}})
		}
	}
	if cfg.Runtime.BaseURL != "" {
		sc := &steering.Client{BaseURL: cfg.Runtime.BaseURL, Token: cfg.Runtime.Token, ExecutionID: cfg.ExecutionID}
		a.BeforeModel = func(hctx context.Context, state *engine.State) error {
			n := len(state.Messages)
			if state.Cursor == 0 && cfg.Steering != nil {
				state.Cursor = cfg.Steering.AfterSeq
			}
			e := sc.BeforeModel(hctx, state)
			if e == nil && len(state.Messages) > n {
				_ = comp.Trace.Record("context.steering", map[string]any{"messages": state.Messages[n:], "incorporated_through_seq": state.Cursor})
			}
			return e
		}
		hook = a.BeforeModel
	}
	var summarizer engine.Model
	if gm, ok := supplied.Model.(*model.GatewayModel); ok {
		sm := &model.GatewayModel{BaseURL: gm.BaseURL, Token: gm.Token, Model: gm.Model, MaxOutputTokens: reserve, Parameters: summaryParameters(cfg.Model.Parameters)}
		sm.OnRequest = func(raw json.RawMessage) error { return comp.Trace.Record("context.summary.request", raw) }
		sm.OnResponse = func(raw json.RawMessage) error { return comp.Trace.Record("context.summary.response", raw) }
		summarizer = sm
	}
	toolDefs := a.Tools.Definitions()
	if toolCatalog != nil {
		toolDefs = toolCatalog()
	}
	inputDefinition := api.ToolDefinition{Name: "agent_request_input", Description: "Request human input", Parameters: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["question","choice","approval"]},"prompt":{"type":"string"},"options":{"type":"array","items":{"type":"object","properties":{"value":{"type":"string"},"label":{"type":"string"}},"required":["value","label"]}}},"required":["kind","prompt"]}`)}
	if !hasToolDefinition(toolDefs, "agent_request_input") {
		toolDefs = append(toolDefs, inputDefinition)
	}
	catalogSnapshot := func() []api.ToolDefinition {
		if toolCatalog == nil {
			return append([]api.ToolDefinition(nil), toolDefs...)
		}
		defs := toolCatalog()
		if !hasToolDefinition(defs, inputDefinition.Name) {
			defs = append(defs, inputDefinition)
		}
		return defs
	}
	if gm, ok := supplied.Model.(*model.GatewayModel); ok {
		gm.ToolDefinitions = catalogSnapshot
	}
	store := contextstore.Store{Root: filepath.Join(root, "workspace")}
	parameterBytes, _ := json.Marshal(cfg.Model.Parameters)
	if cfg.Model.Parameters == nil {
		parameterBytes = nil
	}
	processor.Options = contextpkg.Options{ContextWindowTokens: window, ReserveOutputTokens: reserve, Estimator: contextpkg.ByteEstimator{}, Summarizer: summarizer, ExtraTokens: len(gmToolsJSON(toolDefs)) + len(toolDefs)*64 + len(parameterBytes), OffloadToolResult: func(hctx context.Context, callID, content string) (contextpkg.ToolResultSource, error) {
		ref, err := store.Save(content)
		if err != nil {
			return contextpkg.ToolResultSource{}, err
		}
		return contextpkg.ToolResultSource{CallID: callID, SHA256: ref.SHA256, Path: ref.Path, OriginalBytes: ref.SizeBytes}, nil
	}, OnSummary: func(s contextpkg.SummarySource) { _ = comp.Trace.Record("context.compacted", s) }, OnToolResult: func(s contextpkg.ToolResultSource) { _ = comp.Trace.Record("context.tool_result.offloaded", s) }}
	contextRunner := app.ContextProcessor(processor)
	if toolCatalog != nil {
		contextRunner = contextProcessorFunc(func(hctx context.Context, s *engine.State) error {
			defs := catalogSnapshot()
			processor.ExtraTokens = len(gmToolsJSON(defs)) + len(defs)*64 + len(parameterBytes)
			return processor.Process(hctx, s)
		})
	}
	if h := a.BeforeModel; h != nil {
		a.BeforeModel = func(hctx context.Context, s *engine.State) error {
			if e := h(hctx, s); e != nil {
				return e
			}
			return contextRunner.Process(hctx, s)
		}
	} else {
		a.BeforeModel = contextRunner.Process
	}
	hook = a.BeforeModel
	recovery := func(rctx context.Context, state *engine.State, err error) (bool, error) {
		if !model.ContextLengthExceeded(err) {
			return false, nil
		}
		if e := processor.ForceCompact(rctx, state); e != nil {
			return false, e
		}
		return true, nil
	}
	comp.Loop.Configure(a.Events, hook, recovery)
	msgs := make([]engine.Message, 0, len(cfg.Messages)+len(refs))
	if cfg.Resume != nil {
		msgs = append(msgs, restored.Messages...)
	}
	for _, m := range cfg.Messages {
		if cfg.Resume != nil {
			break
		}
		em := engine.Message{Role: m.Role, Content: ptr(m.Content), ToolCallID: ptr(m.ToolCallID)}
		for _, tc := range m.ToolCalls {
			em.ToolCalls = append(em.ToolCalls, engine.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)})
		}
		msgs = append(msgs, em)
	}
	if cfg.Resume == nil {
		for _, m := range refs {
			msgs = append(msgs, engine.Message{Role: m.Role, Content: ptr(m.Content)})
		}
	}
	if cfg.Resume != nil && cfg.Resume.Answer != nil {
		b, _ := json.Marshal(cfg.Resume.Answer)
		msgs = append(msgs, engine.Message{Role: contracts.RoleUser, Content: string(b)})
	}
	s, runErr := comp.Loop.RunState(ctx, engine.ModelRequest{Messages: msgs, Model: cfg.Model.Name, ReasoningEffort: ptr(cfg.Model.ReasoningEffort)})
	_ = comp.Trace.Record("agent.stage_result", map[string]any{"status": s.Status, "summary": s.Summary, "request": s.Request, "error": fmt.Sprint(runErr)})
	if s.Status == engine.StatusAwaitingInput && s.Request != nil {
		return pauseResult(ctx, output, root, cfg, s, baseline, tr, comp.Versions, comp.Checkpoint)
	}
	if runErr != nil {
		var ce *contextpkg.Error
		if errors.As(runErr, &ce) {
			return resultError(output, s.Cursor, ce.Code, "context")
		}
		return resultError(output, s.Cursor, "agent_execution_failed", fmt.Sprintf("%T", runErr))
	}
	tr.Freeze()
	delivery := contracts.DeliveryOutcome{Status: contracts.DeliveryNotRequested}
	bundle, be := comp.Artifacts.Collect(filepath.Join(root, "workspace"), filepath.Join(root, "output"), baseline)
	if be == nil {
		delivery, be = comp.Artifacts.DeliverContext(ctx, bundle, cfg.ResultBundle)
	}
	if be != nil {
		code := "result_bundle_upload_failed"
		if ae, ok := be.(*artifacts.Error); ok {
			code = ae.Code
		}
		return resultError(output, s.Cursor, code, "artifact_delivery")
	}
	r := contracts.RuntimeResult{Status: contracts.RuntimeOk, Summary: s.Summary, Steering: &contracts.RuntimeSteeringResult{IncorporatedThroughSeq: s.Cursor}, Delivery: delivery}
	b, _ := json.Marshal(r)
	return atomicJSON(output, b)
}

func summaryParameters(in contracts.ModelParameters) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage)
	for _, k := range []string{"temperature", "top_p", "thinking", "reasoning_effort", "max_tokens", "max_completion_tokens"} {
		if v, ok := in[k]; ok {
			out[k] = append(json.RawMessage(nil), v...)
		}
	}
	return out
}

func cloneModelParameters(in contracts.ModelParameters) contracts.ModelParameters {
	if in == nil {
		return nil
	}
	out := make(contracts.ModelParameters, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func hasToolDefinition(defs []api.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func pauseResult(ctx context.Context, output, root string, cfg *contracts.RuntimeConfig, s engine.State, baseline artifacts.Baseline, tr *trace.Recorder, versions map[string]string, cpService app.CheckpointService) error {
	tr.Freeze()
	data, _ := json.Marshal(struct {
		Engine   engine.State       `json:"engine"`
		Baseline artifacts.Baseline `json:"baseline"`
	}{s, baseline})
	cp := filepath.Join(root, "output", "checkpoint.tar.gz")
	if e := cpService.Save(ctx, filepath.Join(root, "workspace"), filepath.Join(root, "output"), cp, checkpoint.Options{RunID: cfg.RunID, Stage: cfg.Stage, Fence: cfg.Fence, Cursor: s.Cursor, PluginVersions: versions, MaxBytes: cfg.Limits.CheckpointMaxBytes, TraceRoot: filepath.Join(root, ".runtime-trace")}, data); e != nil {
		return resultError(output, s.Cursor, "checkpoint_persist_failed", "checkpoint")
	}
	hash, size, e := checkpointHash(cp)
	if e != nil {
		return e
	}
	r := contracts.RuntimeResult{Status: contracts.RuntimeAwaitingInput, Request: &contracts.InputRequest{Kind: s.Request.Kind, Prompt: s.Request.Prompt, Options: toChoices(s.Request.Options)}, Checkpoint: &contracts.CheckpointRef{Path: cp, Format: checkpoint.Format, SHA256: hash, SizeBytes: size}, Steering: &contracts.RuntimeSteeringResult{IncorporatedThroughSeq: s.Cursor}}
	out, _ := json.Marshal(r)
	return atomicJSON(output, out)
}
func toChoices(in []engine.Choice) []contracts.Choice {
	out := make([]contracts.Choice, len(in))
	for i, v := range in {
		out[i] = contracts.Choice{Value: v.Value, Label: v.Label}
	}
	return out
}

type traceSink struct{ r *trace.Recorder }

func (s traceSink) Emit(_ context.Context, e engine.Event) error { return s.r.Record(e.Type, e.Data) }

type multiSink struct{ a, b engine.EventSink }

func (s multiSink) Emit(ctx context.Context, e engine.Event) error {
	_ = s.a.Emit(ctx, e)
	return s.b.Emit(ctx, e)
}
func ptr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func resultError(path string, c int64, code, typ string) error {
	r := contracts.RuntimeResult{Status: contracts.RuntimeError, ErrorCode: code, ErrorType: typ, Steering: &contracts.RuntimeSteeringResult{IncorporatedThroughSeq: c}}
	b, _ := json.Marshal(r)
	if e := atomicJSON(path, b); e != nil {
		return e
	}
	return &executionError{code: code, typ: typ}
}
func atomicJSON(path string, b []byte) error {
	if len(b) > 4<<20 {
		return fmt.Errorf("result too large")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".result-")
	if e != nil {
		return e
	}
	n := f.Name()
	defer os.Remove(n)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(n, path)
}
