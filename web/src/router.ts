import { createRouter, createWebHistory } from 'vue-router'
import { setUnauthorizedHandler } from './api'
import { loadSession, session } from './session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'scripts', component: () => import('./views/ScriptsView.vue'), meta: { title: 'Scripts' } },
    { path: '/scripts/:name', name: 'script', component: () => import('./views/ScriptView.vue'), props: true },
    { path: '/running', name: 'running', component: () => import('./views/RunningView.vue'), meta: { title: 'Running' } },
    { path: '/runs', name: 'runs', component: () => import('./views/RunsView.vue'), meta: { title: 'History' } },
    { path: '/runs/:id', name: 'run', component: () => import('./views/RunView.vue'), props: true },
    { path: '/admin', name: 'admin', component: () => import('./views/AdminView.vue'), meta: { title: 'Admin', admin: true } },
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue'), meta: { public: true, title: 'Sign in' } },
    { path: '/:rest(.*)', name: 'not-found', component: () => import('./views/NotFoundView.vue'), meta: { title: 'Not found' } },
  ],
})

router.beforeEach(async (to) => {
  if (!session.loaded) await loadSession()
  if (!to.meta.public && !session.me) {
    return { name: 'login', query: { next: to.fullPath } }
  }
  if (to.meta.admin && !session.me?.admin) {
    return { name: 'not-found', params: { rest: to.path.slice(1) } }
  }
})

router.afterEach((to) => {
  const page = (to.meta.title as string | undefined) ?? (to.params.name as string | undefined) ?? (to.params.id as string | undefined)
  document.title = page ? `${page} · Runwarden` : 'Runwarden'
})

setUnauthorizedHandler(() => {
  if (!session.me) return
  session.me = null
  const current = router.currentRoute.value
  if (!current.meta.public) void router.push({ name: 'login', query: { next: current.fullPath } })
})
