<script setup lang="ts">
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { onBeforeUnmount, onMounted, ref } from 'vue'

const host = ref<HTMLDivElement>()
let term: Terminal | undefined
let fit: FitAddon | undefined
let observer: ResizeObserver | undefined

onMounted(() => {
  term = new Terminal({
    convertEol: true, // scripts print "\n"; the terminal needs "\r\n"
    disableStdin: true, // input goes through the input line, not keystrokes
    scrollback: 20000,
    fontFamily: "ui-monospace, 'Cascadia Mono', Consolas, 'Liberation Mono', monospace",
    fontSize: 13,
    theme: { background: '#0d1117', foreground: '#e6edf3' },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(host.value!)
  fit.fit()
  observer = new ResizeObserver(() => fit?.fit())
  observer.observe(host.value!)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  term?.dispose()
})

defineExpose({
  write: (data: string) => term?.write(data),
  clear: () => term?.reset(),
})
</script>

<template>
  <div class="terminal-wrap" role="log" aria-label="Script output">
    <div ref="host" style="height: 100%" />
  </div>
</template>
