import { createApp } from 'vue'
import App from './App.vue'
import { api } from './api'
import { migrateStorage } from './lib/storage'
import { initTheme } from './lib/theme'
import { router } from './router'
import './styles.css'

migrateStorage()
void initTheme(api.getUiSettings)
createApp(App).use(router).mount('#app')
