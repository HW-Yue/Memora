// One-off diagnostic for the DSH claude-code product subagent. Delete after use.
import { query } from '/Users/yy/.dsh/profiles/web/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs'
import { spawn } from 'node:child_process'

function passthrough(options) {
  console.log('[spawn] command =', options.command)
  console.log('[spawn] args =', JSON.stringify(options.args))
  console.log('[spawn] env keys =', Object.keys(options.env ?? {}).length)
  const child = spawn(options.command, options.args, {
    cwd: options.cwd,
    env: options.env,
    stdio: ['pipe', 'pipe', 'inherit'],
    signal: options.signal,
  })
  child.on('error', (e) => console.log('[child error]', e?.message))
  child.on('exit', (code, signal) => console.log('[child exit]', code, signal))
  return child
}

const controller = new AbortController()
try {
  const q = query({
    prompt: 'Reply with exactly: PONG',
    options: {
      abortController: controller,
      cwd: '/Users/yy/Developer/Memora',
      model: 'claude-opus-5',
      persistSession: false,
      disallowedTools: ['AskUserQuestion'],
      permissionMode: 'bypassPermissions',
      allowDangerouslySkipPermissions: true,
      spawnClaudeCodeProcess: passthrough,
    },
  })
  console.log('[query] created')
  for await (const message of q) {
    console.log('[message]', message.type, message.subtype ?? '')
    if (message.type === 'result') console.log('[result]', JSON.stringify(message).slice(0, 600))
  }
  console.log('[done]')
} catch (error) {
  console.log('[thrown]', error?.name, error?.message)
  let cause = error?.cause
  let depth = 0
  while (cause && depth < 6) {
    console.log(`[cause ${depth}]`, cause?.name, cause?.message)
    cause = cause?.cause
    depth += 1
  }
  console.log('[stack]', String(error?.stack).slice(0, 1500))
}
