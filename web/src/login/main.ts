// The sign-in page: the only page a visitor without a session gets (served for every path).
import { createApp } from 'vue'
import LoginApp from './LoginApp.vue'
import '../styles.css'

createApp(LoginApp).mount('#app')
