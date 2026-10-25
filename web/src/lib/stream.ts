// Follows a run over the WebSocket, resuming from the last offset after a disconnect.

export interface RunResult {
  status: string
  exit_code?: number
  finished?: string
  output_truncated?: boolean
}

export type StreamState = 'connecting' | 'open' | 'reconnecting' | 'done'

/** The part of WebSocket the stream uses; tests pass a fake. */
export interface SocketLike {
  onopen: ((ev: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: unknown) => void) | null
  onerror: ((ev: unknown) => void) | null
  send(data: string): void
  close(): void
}

export interface StreamHandlers {
  onOutput(data: string): void
  onResult(result: RunResult): void
  onState(state: StreamState): void
  onError?(message: string): void
}

const retryDelays = [1000, 2000, 5000, 10000]

export class RunStream {
  offset = 0
  private socket: SocketLike | null = null
  private finished = false
  private disposed = false
  private attempt = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  private readonly url: (offset: number) => string
  private readonly handlers: StreamHandlers
  private readonly open: (url: string) => SocketLike

  constructor(
    url: (offset: number) => string,
    handlers: StreamHandlers,
    open: (url: string) => SocketLike = (u) => new WebSocket(u) as unknown as SocketLike,
  ) {
    this.url = url
    this.handlers = handlers
    this.open = open
  }

  connect() {
    if (this.disposed || this.finished) return
    this.handlers.onState(this.attempt === 0 ? 'connecting' : 'reconnecting')
    const s = this.open(this.url(this.offset))
    this.socket = s
    s.onopen = () => {
      this.attempt = 0
      this.handlers.onState('open')
    }
    s.onmessage = (ev) => this.message(String(ev.data))
    s.onerror = () => {}
    s.onclose = () => {
      this.socket = null
      if (this.finished || this.disposed) return
      const delay = retryDelays[Math.min(this.attempt, retryDelays.length - 1)]
      this.attempt++
      this.handlers.onState('reconnecting')
      this.timer = setTimeout(() => this.connect(), delay)
    }
  }

  private message(raw: string) {
    let msg: { type?: string; data?: string; offset?: number; message?: string } & RunResult
    try {
      msg = JSON.parse(raw)
    } catch {
      return
    }
    switch (msg.type) {
      case 'output':
        if (typeof msg.offset === 'number') this.offset = msg.offset
        this.handlers.onOutput(msg.data ?? '')
        break
      case 'result':
        this.finished = true
        this.handlers.onResult({
          status: msg.status,
          exit_code: msg.exit_code,
          finished: msg.finished,
          output_truncated: msg.output_truncated,
        })
        this.handlers.onState('done')
        this.socket?.close()
        break
      case 'error':
        this.handlers.onError?.(msg.message ?? 'error')
        break
    }
  }

  /** Sends a command (input, close_input, stop, kill). Returns false when not connected. */
  send(msg: { type: string; line?: string; secret?: boolean }): boolean {
    if (!this.socket) return false
    this.socket.send(JSON.stringify(msg))
    return true
  }

  dispose() {
    this.disposed = true
    clearTimeout(this.timer)
    this.socket?.close()
    this.socket = null
  }
}
