export function chatToSdk(messages) {
  const result = [];
  for (const message of messages || []) {
    if (message.role === 'assistant') {
      if (message.reasoning_content) result.push({type: 'reasoning', content: [], rawContent: [{type: 'reasoning_text', text: message.reasoning_content}]});
      result.push({type: 'message', role: 'assistant', content: [{type: 'output_text', text: message.content || ''}], status: 'completed'});
      for (const call of message.tool_calls || []) result.push({type: 'function_call', callId: call.id, name: call.function.name, arguments: call.function.arguments || '{}', status: 'completed'});
    } else if (message.role === 'tool') {
      const call = result.findLast((item) => item.type === 'function_call' && item.callId === message.tool_call_id);
      result.push({type: 'function_call_result', callId: message.tool_call_id, name: call?.name, output: message.content || '', status: 'completed'});
    }
    else if (message.role === 'user' || message.role === 'system') result.push({role: message.role, content: typeof message.content === 'string' ? message.content : JSON.stringify(message.content)});
    else if (message.role === 'developer') result.push({type: 'unknown', providerData: {...message}});
  }
  return result;
}

export function sdkToChat(history) {
  const messages = [];
  let pendingReasoning = '';
  for (const item of history || []) {
    if (item.role === 'system' || item.role === 'user') messages.push({role: item.role, content: typeof item.content === 'string' ? item.content : item.content?.map?.((part) => part?.text || '').join('') || ''});
    else if (item.type === 'reasoning') {
      const text = item.rawContent?.map((part) => part?.text || '').join('') || '';
      pendingReasoning = text;
    } else if (item.type === 'message' && item.role === 'assistant') {
      const content = Array.isArray(item.content) ? item.content.map((part) => part?.text || '').join('') : item.content;
      messages.push({role: 'assistant', content: content || '', ...(pendingReasoning ? {reasoning_content: pendingReasoning} : {})});
      pendingReasoning = '';
    } else if (item.type === 'function_call') {
      let last = messages.at(-1);
      if (!last || last.role !== 'assistant') { last = {role: 'assistant', content: ''}; messages.push(last); }
      if (pendingReasoning) { last.reasoning_content = pendingReasoning; pendingReasoning = ''; }
      last.tool_calls ??= [];
      last.tool_calls.push({id: item.callId, type: 'function', function: {name: item.name, arguments: item.arguments || '{}'}});
    } else if (item.type === 'function_call_result') messages.push({role: 'tool', tool_call_id: item.callId, content: typeof item.output === 'string' ? item.output : JSON.stringify(item.output)});
    else if (item.type === 'unknown' && item.providerData?.role === 'developer') messages.push({...item.providerData});
  }
  return messages;
}
