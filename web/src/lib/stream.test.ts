import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RunStream, type SocketLike, type StreamState } from './stream'

class FakeSocket implements SocketLike {
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev: unknown) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null
  sent: string[] = []
  closed = false
  readonly url: string
  constructor(url: string) {
    this.url = url
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.closed = true
  }
  emit(msg: object) {
    this.onmessage?.({ data: JSON.stringify(msg) })
  }
  drop() {
    this.onclose?.({})
  }
}

describe('RunStream', () => {
  let sockets: FakeSocket[]
  let output: string
  let states: StreamState[]
  let result: unknown

  beforeEach(() => {
    vi.useFakeTimers()
    sockets = []
    output = ''
    states = []
    result = undefined
  })
  afterEach(() => vi.useRealTimers())

  const start = () => {
    const s = new RunStream(
      (offset) => `ws://x/ws?offset=${offset}`,
      {
        onOutput: (d) => (output += d),
        onResult: (r) => (result = r),
        onState: (st) => states.push(st),
      },
      (url) => {
        const f = new FakeSocket(url)
        sockets.push(f)
        return f
      },
    )
    s.connect()
    return s
  }

  it('resumes from the last offset after a disconnect', () => {
    const s = start()
    sockets[0].onopen?.({})
    sockets[0].emit({ type: 'output', data: 'hello ', offset: 6 })
    sockets[0].drop()
    expect(states).toEqual(['connecting', 'open', 'reconnecting'])

    vi.advanceTimersByTime(1000)
    expect(sockets[1].url).toBe('ws://x/ws?offset=6')
    sockets[1].emit({ type: 'output', data: 'world', offset: 11 })
    sockets[1].emit({ type: 'result', status: 'finished', exit_code: 0 })
    expect(output).toBe('hello world')
    expect(result).toEqual({ status: 'finished', exit_code: 0, finished: undefined, output_truncated: undefined })
    expect(sockets[1].closed).toBe(true)

    sockets[1].drop()
    vi.advanceTimersByTime(20000)
    expect(sockets).toHaveLength(2) // no reconnect after the result
    expect(s.offset).toBe(11)
  })

  it('backs off between attempts and stops when disposed', () => {
    const s = start()
    sockets[0].drop()
    vi.advanceTimersByTime(1000)
    sockets[1].drop()
    vi.advanceTimersByTime(1999)
    expect(sockets).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(sockets).toHaveLength(3)
    s.dispose()
    sockets[2].drop()
    vi.advanceTimersByTime(60000)
    expect(sockets).toHaveLength(3)
  })

  it('sends commands only while connected', () => {
    const s = start()
    expect(s.send({ type: 'input', line: 'yes' })).toBe(true)
    expect(JSON.parse(sockets[0].sent[0])).toEqual({ type: 'input', line: 'yes' })
    sockets[0].drop()
    expect(s.send({ type: 'stop' })).toBe(false)
  })
})
