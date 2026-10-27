import { createApp } from 'vue'
import App from './App.vue'
import { api } from './api'
import { initTheme } from './lib/theme'
import { router } from './router'
import './styles.css'

void initTheme(api.getUiSettings)
createApp(App).use(router).mount('#app')
