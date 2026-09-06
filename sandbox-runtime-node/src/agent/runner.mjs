import {buildCheckpoint, restoreCheckpoint} from '../recovery/checkpoint.mjs';
import {loadRunConfig} from '../protocol/run-config.mjs';
import {writeResult} from '../protocol/result.mjs';
import {failure} from '../protocol/errors.mjs';
import {consumeSteering, emitEvent, emitToolEvent} from './platform-client.mjs';
import {Agent, MaxTurnsExceededError, Runner, ToolCallError, tool} from '@openai/agents';
import {createGatewayModel} from './sdk-model.mjs';
import {chatToSdk, sdkToChat} from './chat-history.mjs';
import {workspaceBaseline} from '../artifacts/collector.mjs';
import {deliverArtifacts} from '../artifacts/deliver.mjs';
import {prepareResources} from '../resources/prepare.mjs';
import {createRemoteTools} from '../tools/remote/registry.mjs';
import {builtinDefinitions, executeBuiltin} from '../tools/builtin.mjs';

function deadlineFor(seconds) {
  const end = Date.now() + seconds * 1000;
  return {
    remaining: () => Math.max(0, Math.ceil((end - Date.now()) / 1000)),
    expired: () => Date.now() >= end,
  };
}

function parseInputArgs(raw) {
  let args;
  try {
    args = JSON.parse(raw || '{}');
  } catch {
    throw failure('runtime_protocol_invalid');
  }
  if (
    !args ||
    !['question', 'choice', 'approval'].includes(args.kind) ||
    typeof args.prompt !== 'string' ||
    !args.prompt.trim()
  )
    throw failure('runtime_protocol_invalid');
  if (
    args.options !== undefined &&
    (!Array.isArray(args.options) ||
      args.options.length > 32 ||
      args.options.some(
        (option) => !option || typeof option.value !== 'string' || typeof option.label !== 'string',
      ))
  )
    throw failure('runtime_protocol_invalid');
  if (args.kind === 'choice' && (!Array.isArray(args.options) || !args.options.length))
    throw failure('runtime_protocol_invalid');
  return args;
}

async function run(root, cfg, cancel, state) {
  const deadline = deadlineFor(cfg.limits.remaining_execution_seconds);
  const timeout = setTimeout(() => {
    state.timedOut = true;
    cancel.abort();
  }, cfg.limits.remaining_execution_seconds * 1000);
  const sequence = {value: 0};
  let messages = Array.isArray(cfg.messages) ? structuredClone(cfg.messages) : [];
  let baseline;
  let remote;
  let cursor = cfg.steering?.after_seq || 0;
  state.lastCursor = cursor;
  try {
    if (cfg.resume) {
      let restored;
      try {
        restored = restoreCheckpoint(cfg.resume.checkpoint_path, root, {
          maxBytes: Number(cfg.limits.checkpoint_max_bytes || 0),
          runId: cfg.run_id,
          beforeStage: cfg.stage,
        });
      } catch {
        throw failure('resume_failed');
      }
      if (
        restored.cursor !== cursor ||
        restored.state?.cursor !== cursor ||
        !Array.isArray(restored.state?.messages)
      ) {
        throw failure('steering_checkpoint_mismatch');
      }
      messages = restored.state.messages;
      baseline = restored.baseline || {workspace: {}};
      const resources = await prepareResources({
        root,
        config: cfg,
        signal: cancel.signal,
        deadline,
        resume: true,
      });
      if (
        resources.instructions &&
        !messages.some(
          (message) => message.role === 'system' && message.content === resources.instructions,
        )
      )
        messages.unshift({role: 'system', content: resources.instructions});
      if (cfg.resume.answer !== undefined)
        messages.push({
          role: 'user',
          content:
            typeof cfg.resume.answer === 'string'
              ? cfg.resume.answer
              : JSON.stringify(cfg.resume.answer),
        });
    }
    if (!cfg.resume) {
      const resources = await prepareResources({
        root,
        config: cfg,
        signal: cancel.signal,
        deadline,
      });
      if (
        resources.instructions &&
        !messages.some(
          (message) => message.role === 'system' && message.content === resources.instructions,
        )
      )
        messages.unshift({role: 'system', content: resources.instructions});
    }
    if (!baseline) baseline = workspaceBaseline(`${root}/workspace`);
    remote = await createRemoteTools({
      config: cfg.tools || [],
      signal: cancel.signal,
      deadline,
    });
    const toolDefinitions = [...builtinDefinitions(), ...remote.definitions];
    let pendingInput;
    const steers = [];
    const mergeSteering = (input) => {
      const result = [...input];
      let offset = 0;
      for (const entry of steers) {
        result.splice(entry.index + offset, 0, ...entry.items);
        offset += entry.items.length;
      }
      return result;
    };
    const sdkTools = [
      {
        ...tool({
          name: 'agent_request_input',
          description: 'Ask the user for input and pause this run.',
          parameters: {
            type: 'object', additionalProperties: false,
            properties: {
              kind: {type: 'string', enum: ['question', 'choice', 'approval']},
              prompt: {type: 'string'},
              options: {
                type: 'array', items: {
                  type: 'object', additionalProperties: false,
                  properties: {value: {type: 'string'}, label: {type: 'string'}},
                  required: ['value', 'label'],
                },
              },
            },
            required: ['kind', 'prompt'],
          },
          strict: false,
          needsApproval: false,
          execute: async (_input, _ctx, details) => {
            const callId = details?.toolCall?.callId;
            await emitToolEvent(cfg, 'agent.tool_call', callId, 'agent_request_input', 'started', deadline, sequence, cancel);
            if (pendingInput) throw failure('capability_unsupported');
            pendingInput = parseInputArgs(details?.toolCall?.arguments || '{}');
            await emitToolEvent(cfg, 'agent.tool_result', callId, 'agent_request_input', 'awaiting_input', deadline, sequence, cancel, {status: 'awaiting_input'});
            return {status: 'awaiting_input'};
          },
          errorFunction: null,
        }),
        isEnabled: async () => true,
      },
      ...toolDefinitions.map((definition) => {
        const name = definition.function.name;
        return {
          ...tool({
            name,
            description: definition.function.description || name,
            parameters: definition.function.parameters,
            strict: false,
            needsApproval: false,
            execute: async (_input, _ctx, details) => {
              const callId = details?.toolCall?.callId;
              const args = details?.toolCall?.arguments || '{}';
              await emitToolEvent(cfg, 'agent.tool_call', callId, name, 'started', deadline, sequence, cancel);
              const known = builtinDefinitions().some((item) => item.function.name === name);
              const result = known ? executeBuiltin(root, name, args) : await remote.execute(name, args);
              await emitToolEvent(cfg, 'agent.tool_result', callId, name, result.ok ? 'completed' : 'failed', deadline, sequence, cancel, result);
              return JSON.stringify(result);
            },
            errorFunction: null,
          }),
          isEnabled: async () => true,
        };
      }),
    ];
    let finalText = '';
    const model = createGatewayModel(cfg, deadline, cancel, {
      onReasoning: (delta) => emitEvent(cfg, 'agent.reasoning', delta, deadline, sequence, cancel),
    });
    const beforeModel = async ({modelData}) => {
      const before = messages.length;
      cursor = await consumeSteering(cfg, messages, cursor, deadline, cancel);
      state.lastCursor = cursor;
      const added = messages.slice(before);
      if (added.length) steers.push({index: modelData.input.length, items: chatToSdk(added)});
      return {...modelData, input: mergeSteering(modelData.input)};
    };
    const agent = new Agent({
      name: 'platform-agent', instructions: '', model, tools: sdkTools,
      modelSettings: {
        reasoning: cfg.model.reasoning_effort ? {effort: cfg.model.reasoning_effort} : undefined,
        retry: {maxRetries: 0},
      },
      toolUseBehavior: {stopAtToolNames: ['agent_request_input']},
    });
    const runner = new Runner({tracingDisabled: true});
    try {
      const streamed = await runner.run(agent, chatToSdk(messages), {
        maxTurns: 40, signal: cancel.signal,
        stream: true,
        callModelInputFilter: beforeModel,
        toolExecution: {maxFunctionToolConcurrency: 1},
        toolNotFoundBehavior: 'return_error_to_model',
        toolErrorFormatter: ({defaultMessage}) => JSON.stringify({ok: false, error_type: 'ToolError', error: defaultMessage}),
      });
      for await (const event of streamed) {
        if (event.type === 'raw_model_stream_event' && event.data?.type === 'output_text_delta')
          await emitEvent(cfg, 'agent.token', event.data.delta || '', deadline, sequence, cancel);
      }
      await streamed.completed;
      if (streamed.interruptions?.length) throw failure('capability_unsupported');
      if (!pendingInput && streamed.newItems?.some((item) => item.type === 'tool_call_item' && item.rawItem?.name === 'agent_request_input'))
        throw failure('runtime_protocol_invalid');
      messages = sdkToChat(mergeSteering(streamed.history));
      finalText = typeof streamed.finalOutput === 'string' ? streamed.finalOutput : '';
    } catch (error) {
      if (error instanceof ToolCallError && error.error) throw error.error;
      if (error instanceof MaxTurnsExceededError) throw failure('execution_timeout');
      throw error;
    }
    if (pendingInput) {
      const checkpoint = buildCheckpoint({workspace: `${root}/workspace`, output: `${root}/output`, state: messages, baseline, cursor, cfg, dest: `${root}/output/checkpoint.tar.gz`});
      writeResult(root, {status: 'awaiting_input', request: pendingInput, checkpoint, steering: {incorporated_through_seq: cursor}});
      return;
    }
    const delivery = await deliverArtifacts({root, destination: cfg.result_bundle, baseline, signal: cancel.signal, deadline});
    writeResult(root, {status: 'ok', summary: finalText, delivery, steering: {incorporated_through_seq: cursor}});
  } finally {
    clearTimeout(timeout);
    await remote?.close();
  }
}

export async function executeRun(root) {
  const cancel = new AbortController();
  const state = {timedOut: false, lastCursor: 0};
  const abort = () => cancel.abort();
  process.on('SIGTERM', abort);
  process.on('SIGINT', abort);
  const knownErrors = new Set([
    'runtime_protocol_invalid',
    'run_cancelled',
    'execution_timeout',
    'capability_unsupported',
    'steering_checkpoint_mismatch',
    'resume_failed',
    'steering_delivery_failed',
    'resource_limit_exceeded',
    'agent_execution_failed',
    'artifact_destination_required',
    'result_bundle_upload_failed',
    'resource_download_failed',
    'resource_download_too_large',
    'resource_size_mismatch',
    'resource_hash_mismatch',
    'resource_unsafe_path',
    'resource_duplicate_path',
    'resource_invalid_entry',
    'skill_unsupported_format',
    'skill_archive_unsafe',
    'skill_limit_exceeded',
    'skill_missing_manifest',
    'skill_rebuild_failed',
  ]);
  try {
    const config = loadRunConfig(root);
    await run(root, config, cancel, state);
    return true;
  } catch (error) {
    const code = state.timedOut
      ? 'execution_timeout'
      : cancel.signal.aborted
        ? 'run_cancelled'
        : knownErrors.has(error.code)
          ? error.code
          : 'agent_execution_failed';
    writeResult(root, {
      status: 'error',
      error_code: code,
      error_type: 'RuntimeError',
      steering: {incorporated_through_seq: state.lastCursor},
    });
    return false;
  } finally {
    process.removeListener('SIGTERM', abort);
    process.removeListener('SIGINT', abort);
  }
}
