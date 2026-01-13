import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/',
      redirect: '/dashboard'
    },
    {
      path: '/dashboard',
      name: 'Dashboard',
      component: () => import('@/views/Dashboard.vue'),
      meta: { title: '仪表盘' }
    },
    {
      path: '/account',
      name: 'AccountManage',
      component: () => import('@/views/AccountManage.vue'),
      meta: { title: '云账号管理' }
    },
    {
      path: '/instance-parameter',
      name: 'InstanceParameter',
      component: () => import('@/views/InstanceParameter.vue'),
      meta: { title: 'Instance参数管理' }
    },
    {
      path: '/nodegroup',
      name: 'NodeGroupList',
      component: () => import('@/views/NodeGroupList.vue'),
      meta: { title: 'NodeGroup管理' }
    },
    {
      path: '/instance',
      name: 'InstanceList',
      component: () => import('@/views/InstanceList.vue'),
      meta: { title: 'Instance管理' }
    }
  ]
})

export default router
