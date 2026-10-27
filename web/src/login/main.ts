// The sign-in page: the only page a visitor without a session gets (served for every path).
import { createApp } from 'vue'
import { api } from '../api'
import { initTheme } from '../lib/theme'
import LoginApp from './LoginApp.vue'
import '../styles.css'

void initTheme(api.getUiSettings)
createApp(LoginApp).mount('#app')
