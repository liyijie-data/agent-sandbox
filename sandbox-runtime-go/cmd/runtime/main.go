package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"agent-platform/internal/contracts"
	runtime "agent-platform/sandbox-runtime-go"
	"agent-platform/sandbox-runtime-go/app"
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/model"
	"agent-platform/sandbox-runtime-go/plugin"
	"agent-platform/sandbox-runtime-go/transport"
)

func main() {
	serve := flag.Bool("serve", false, "serve runtime HTTP transport")
	port := flag.Int("port", 8888, "HTTP port")
	config := flag.String("config", "/app/run.json", "execution configuration")
	plugins := flag.String("plugins-config", "/app/plugins.json", "compiled plugin composition")
	flag.Parse()
	if !*serve {
		runChild(*config, *plugins)
		return
	}
	if c, e := plugin.LoadConfig(*plugins); e == nil {
		if e := app.ValidateProfile(c); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		if b, e := json.Marshal(c); e == nil {
			os.Setenv("AGENT_RUNTIME_PLUGINS_SNAPSHOT", base64.StdEncoding.EncodeToString(b))
		}
	} else {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	m := contracts.Manifest{ContractVersion: contracts.RuntimeContractVersion, ImageVersion: "1.0.0", Entrypoint: []string{"/app/bin/runtime"}, StateFormat: "go-runtime/1", Capabilities: []string{contracts.CapabilityCoreExec, contracts.CapabilityCoreEvents, contracts.CapabilityCoreCancel, contracts.CapabilityCorePauseResume, contracts.CapabilityCoreSteering, contracts.CapabilityFiles, contracts.CapabilitySkills, contracts.CapabilityOpenAPI, contracts.CapabilityMCP, contracts.CapabilityArtifacts}}
	t := transport.NewTransport("/app", *config, m)
	defer t.Close()
	s := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", *port), Handler: t.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = s.Close() }()
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runChild(path, pluginsPath string) {
	f, e := os.Open(path)
	if e != nil {
		fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
		os.Exit(1)
	}
	b, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	_ = f.Close()
	if e != nil || int64(len(b)) > 4<<20 {
		fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
		os.Exit(1)
	}
	if !json.Valid(b) {
		fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
		os.Exit(1)
	}
	cfg, validationErr := contracts.DecodeRuntimeConfig(b)
	if validationErr != nil {
		fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
		os.Exit(1)
	}
	if snap := os.Getenv("AGENT_RUNTIME_PLUGINS_SNAPSHOT"); snap != "" {
		raw, de := base64.StdEncoding.DecodeString(snap)
		pc, pe := plugin.DecodeConfig(raw)
		if de != nil || pe != nil || app.ValidateProfile(pc) != nil {
			fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
			os.Exit(1)
		}
	} else {
		pc, le := plugin.LoadConfig(pluginsPath)
		if le != nil {
			fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
			os.Exit(1)
		}
		if le = app.ValidateProfile(pc); le != nil {
			fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
			os.Exit(1)
		}
		pb, le := json.Marshal(pc)
		if le != nil || os.Setenv("AGENT_RUNTIME_PLUGINS_SNAPSHOT", base64.StdEncoding.EncodeToString(pb)) != nil {
			fmt.Fprintln(os.Stderr, "runtime_protocol_invalid")
			os.Exit(1)
		}
	}
	m := &model.GatewayModel{BaseURL: cfg.Model.BaseURL, Token: cfg.Model.Token, Model: cfg.Model.Name}
	a := &engine.Agent{Model: m, Tools: mustTools()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if e = runtime.ExecuteRun(ctx, cfg, a, "/app/output/result.json"); e != nil {
		os.Exit(1)
	}
}
func mustTools() *engine.ToolRegistry {
	r, e := engine.NewToolRegistry(nil)
	if e != nil {
		panic(e)
	}
	return r
}
